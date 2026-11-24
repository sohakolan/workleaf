package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const spaceDir = "workleaf-space"

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

// currentWorkspace returns the workspace containing cwd, if any.
func currentWorkspace(root, cwd string) (string, bool) {
	space := filepath.Join(root, spaceDir)
	for dir := cwd; strings.HasPrefix(dir, space+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if isWorkspace(dir) {
			return dir, true
		}
	}
	return "", false
}

func create(root, name string) (err error) {
	if err := validName(name); err != nil {
		return err
	}
	cfg, err := loadConfig(root)
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

	return runHooks(cfg.hooks["create"], root, ws, name)
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
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	if err := runHooks(cfg.hooks["remove"], root, ws, name); err != nil {
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
