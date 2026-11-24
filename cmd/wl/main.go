// Command wl creates isolated copy-on-write workspaces of a project.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	spaceDir   = "workleaf-space"
	configFile = "workleaf.conf"
	// Marks a directory as a workspace, so `ls` and `rm` can tell it apart from
	// the intermediate folders of names like feature/test. Invisible to tools.
	markerAttr = "com.workleaf.workspace"
)

const usage = `usage:
  wl init            make the current directory a workleaf project (creates workleaf.conf)
  wl create <name>   create a workspace in <project>/workleaf-space/<name>
  wl rm <name>       run remove hooks, then delete the workspace
  wl ls              list workspaces`

const confTemplate = `# Workleaf hooks. Commands run with sh in the workspace directory,
# with WL_NAME, WL_PATH and WL_SOURCE set.

[create]
# run = npm install

[remove]
# run = docker compose down
`

func main() {
	cwd, err := os.Getwd()
	if err == nil {
		err = run(cwd, os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wl:", err)
		os.Exit(1)
	}
}

func run(cwd string, args []string) error {
	if len(args) == 1 && args[0] == "init" {
		return initProject(cwd)
	}
	root, err := findRoot(cwd)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 2 && args[0] == "create":
		return create(root, args[1])
	case len(args) == 2 && args[0] == "rm":
		return remove(root, args[1])
	case len(args) == 1 && args[0] == "ls":
		names, err := list(root)
		for _, n := range names {
			fmt.Println(n)
		}
		return err
	}
	return errors.New(usage)
}

func initProject(dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, configFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err // includes "file exists"
	}
	_, err = f.WriteString(confTemplate)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// findRoot returns the nearest directory holding workleaf.conf, like git finds .git.
// Configs inside a workspace are cloned copies, so they are skipped in favor of the main project.
func findRoot(dir string) (string, error) {
	found := ""
	for {
		if found == "" {
			if _, err := os.Stat(filepath.Join(dir, configFile)); err == nil {
				found = dir
			}
		}
		if isWorkspace(dir) {
			found = ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if found == "" {
		return "", errors.New("not a workleaf project (or any parent): run `wl init` at the project root")
	}
	return found, nil
}

func create(root, name string) (err error) {
	if err := validName(name); err != nil {
		return err
	}
	hooks, err := loadHooks(root)
	if err != nil {
		return err
	}
	space := filepath.Join(root, spaceDir)
	ws := filepath.Join(space, name)
	if _, err := os.Lstat(ws); err == nil {
		return fmt.Errorf("%s already exists", ws)
	}
	for dir := filepath.Dir(ws); dir != space; dir = filepath.Dir(dir) {
		if isWorkspace(dir) {
			return fmt.Errorf("%q would be inside workspace %q", name, rel(space, dir))
		}
	}
	if err := excludeFromGit(root); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(ws), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(ws, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(ws)
			removeEmptyParents(space, ws)
		}
	}()
	if err := unix.Setxattr(ws, markerAttr, []byte("1"), 0); err != nil {
		return err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == spaceDir {
			continue // cloning it would make the project clone itself
		}
		src, dst := filepath.Join(root, e.Name()), filepath.Join(ws, e.Name())
		// Clones whole directory trees in one call; never falls back to a real copy.
		if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
			return fmt.Errorf("clone %s: %w (workspaces need an APFS volume)", e.Name(), err)
		}
	}

	return runHooks(hooks["create"], root, ws, name)
}

func remove(root, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	space := filepath.Join(root, spaceDir)
	ws := filepath.Join(space, name)
	if !isWorkspace(ws) {
		return fmt.Errorf("no workspace named %q", name)
	}
	hooks, err := loadHooks(root)
	if err != nil {
		return err
	}
	if err := runHooks(hooks["remove"], root, ws, name); err != nil {
		return fmt.Errorf("%w (workspace kept)", err)
	}
	if err := os.RemoveAll(ws); err != nil {
		return err
	}
	removeEmptyParents(space, ws)
	return nil
}

func list(root string) ([]string, error) {
	space := filepath.Join(root, spaceDir)
	var names []string
	err := filepath.WalkDir(space, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == space {
			return nil
		}
		if err != nil || !d.IsDir() || path == space {
			return err
		}
		if isWorkspace(path) {
			names = append(names, rel(space, path))
			return filepath.SkipDir
		}
		return nil
	})
	return names, err
}

// validName keeps names inside workleaf-space: relative, no "." or ".." segments.
func validName(name string) error {
	if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name {
		return fmt.Errorf("invalid workspace name %q", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("invalid workspace name %q", name)
		}
	}
	return nil
}

func isWorkspace(dir string) bool {
	_, err := unix.Getxattr(dir, markerAttr, nil)
	return err == nil
}

func rel(base, path string) string {
	r, _ := filepath.Rel(base, path)
	return r
}

// removeEmptyParents deletes the now-empty intermediate folders of a name like feature/test.
func removeEmptyParents(space, ws string) {
	for dir := filepath.Dir(ws); dir != space; dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil { // fails when not empty
			return
		}
	}
}

// excludeFromGit adds workleaf-space to .git/info/exclude, leaving .gitignore untouched.
func excludeFromGit(root string) error {
	gitDir := filepath.Join(root, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return nil // not a git repo, or a linked worktree/submodule: leave it alone
	}
	path := filepath.Join(gitDir, "info", "exclude")
	line := "/" + spaceDir + "/"
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		line = "\n" + line
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// loadHooks reads workleaf.conf: [create] / [remove] sections with `run = <command>` lines.
func loadHooks(root string) (map[string][]string, error) {
	hooks := map[string][]string{}
	f, err := os.Open(filepath.Join(root, configFile))
	if errors.Is(err, fs.ErrNotExist) {
		return hooks, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	section := ""
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case line == "[create]" || line == "[remove]":
			section = strings.Trim(line, "[]")
		default:
			key, cmd, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(key) != "run" || section == "" || strings.TrimSpace(cmd) == "" {
				return nil, fmt.Errorf("%s:%d: expected [create], [remove] or `run = <command>` in a section", configFile, n)
			}
			hooks[section] = append(hooks[section], strings.TrimSpace(cmd))
		}
	}
	return hooks, sc.Err()
}

func runHooks(cmds []string, root, ws, name string) error {
	for _, c := range cmds {
		cmd := exec.Command("sh", "-c", c)
		cmd.Dir = ws
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), "WL_NAME="+name, "WL_PATH="+ws, "WL_SOURCE="+root)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("hook %q failed: %w", c, err)
		}
	}
	return nil
}
