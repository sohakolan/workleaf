// Command wl creates isolated copy-on-write workspaces of a project.
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"unsafe"

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
  wl ls              list workspaces
  wl status          current workspace: changed files, real disk usage, git branch
                     (outside a workspace: a summary of every workspace)
  wl info            current workspace in detail, with the list of changed files`

const confTemplate = `# Workleaf hooks. Commands run with sh in the workspace directory,
# with WL_NAME, WL_PATH and WL_SOURCE set.

[create]
# run = npm install

[remove]
# run = docker compose down

[status]
# Hidden from the changes listed by wl status and wl info (disk usage still counts everything).
# "name" or "*.log" matches any file or folder name; "dir/sub/*" matches from the project root.
# Several patterns per line, separated by spaces.
ignore = .git
# ignore = .next *.log
# ignore-gitignored = true
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
	case len(args) == 1 && args[0] == "status":
		return status(root, cwd, os.Stdout)
	case len(args) == 1 && args[0] == "info":
		return info(root, cwd, os.Stdout)
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

type config struct {
	hooks            map[string][]string // "create" / "remove" → commands
	ignore           []string            // patterns hidden from status change lists
	ignoreGitignored bool
}

// loadConfig reads workleaf.conf: [create] / [remove] sections with `run = <command>` lines,
// and a [status] section with `ignore = <patterns...>` and `ignore-gitignored = true|false` lines.
func loadConfig(root string) (config, error) {
	cfg := config{hooks: map[string][]string{}}
	f, err := os.Open(filepath.Join(root, configFile))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	defer f.Close()

	section := ""
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if line == "[create]" || line == "[remove]" || line == "[status]" {
			section = strings.Trim(line, "[]")
			continue
		}
		key, val, _ := strings.Cut(line, "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		bad := func(want string) error {
			return fmt.Errorf("%s:%d: expected %s", configFile, n, want)
		}
		switch {
		case section == "":
			return cfg, bad("[create], [remove] or [status] before this line")
		case section != "status":
			if key != "run" || val == "" {
				return cfg, bad("`run = <command>`")
			}
			cfg.hooks[section] = append(cfg.hooks[section], val)
		case key == "ignore":
			patterns := strings.Fields(val) // several patterns per line, separated by spaces
			if len(patterns) == 0 {
				return cfg, bad("`ignore = <pattern> [<pattern>...]`")
			}
			for _, p := range patterns {
				p = strings.Trim(p, "/")
				if _, err := path.Match(p, ""); p == "" || err != nil {
					return cfg, bad(fmt.Sprintf("a valid pattern, not %q", p))
				}
				cfg.ignore = append(cfg.ignore, p)
			}
		case key == "ignore-gitignored" && (val == "true" || val == "false"):
			cfg.ignoreGitignored = val == "true"
		default:
			return cfg, bad("`ignore = <pattern>` or `ignore-gitignored = true|false`")
		}
	}
	return cfg, sc.Err()
}

// ignored reports whether rel (slash-separated) matches an ignore pattern. A pattern without "/"
// matches any file or folder name; with "/" it matches from the root. A matched folder hides its content.
func ignored(patterns []string, rel string) bool {
	segs := strings.Split(rel, "/")
	for _, p := range patterns {
		for i := range segs {
			candidate := segs[i]
			if strings.Contains(p, "/") {
				candidate = strings.Join(segs[:i+1], "/")
			}
			if ok, _ := path.Match(p, candidate); ok {
				return true
			}
		}
	}
	return false
}

// gitIgnored returns the paths among rels that git ignores in ws (tracked files are never ignored).
func gitIgnored(ws string, rels []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(rels) == 0 {
		return out, nil
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		return out, nil // not a git repository: nothing is gitignored
	}
	cmd := exec.Command("git", "-C", ws, "check-ignore", "--stdin", "-z")
	cmd.Stdin = strings.NewReader(strings.Join(rels, "\x00") + "\x00")
	res, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) { // 1 = nothing ignored
		return nil, fmt.Errorf("git check-ignore: %w", err)
	}
	for _, p := range strings.Split(string(res), "\x00") {
		if p != "" {
			out[p] = true
		}
	}
	return out, nil
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

type wsStatus struct {
	modified, added, deleted []string // sorted paths relative to the workspace
	disk                     int64    // bytes not shared with the source or any other clone
}

func (st wsStatus) changed() int { return len(st.modified) + len(st.added) + len(st.deleted) }

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

// gitBranch returns the workspace's current branch, "(detached)", or "" without git.
func gitBranch(ws string) string {
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		return "" // don't let git pick up a repository above the workspace
	}
	// symbolic-ref also works before the first commit; it exits 1 only on a detached HEAD.
	out, err := exec.Command("git", "-C", ws, "symbolic-ref", "--short", "-q", "HEAD").Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(string(out))
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return "(detached)"
	}
	return ""
}

func status(root, cwd string, w io.Writer) error {
	ws, ok := currentWorkspace(root, cwd)
	if !ok {
		return statusAll(root, w)
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	src, err := walkFiles(root)
	if err != nil {
		return err
	}
	st, err := workspaceStatus(src, ws, cfg)
	if err != nil {
		return err
	}
	fmt.Fprint(w, rel(filepath.Join(root, spaceDir), ws))
	if b := gitBranch(ws); b != "" {
		fmt.Fprintf(w, " on branch %s", b)
	}
	fmt.Fprintf(w, "\n%d files changed, %s on disk\n", st.changed(), humanSize(st.disk))
	return nil
}

func info(root, cwd string, w io.Writer) error {
	ws, ok := currentWorkspace(root, cwd)
	if !ok {
		return errors.New("not inside a workspace (run `wl status` for a summary of all workspaces)")
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	src, err := walkFiles(root)
	if err != nil {
		return err
	}
	st, err := workspaceStatus(src, ws, cfg)
	if err != nil {
		return err
	}
	branch := gitBranch(ws)
	if branch == "" {
		branch = "-"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Workspace\t%s\n", rel(filepath.Join(root, spaceDir), ws))
	fmt.Fprintf(tw, "Path\t%s\n", ws)
	fmt.Fprintf(tw, "Source\t%s\n", root)
	fmt.Fprintf(tw, "Branch\t%s\n", branch)
	fmt.Fprintf(tw, "Disk\t%s (%d bytes not shared with the source)\n", humanSize(st.disk), st.disk)
	fmt.Fprintf(tw, "Changes\t%d modified, %d added, %d deleted\n", len(st.modified), len(st.added), len(st.deleted))
	if err := tw.Flush(); err != nil {
		return err
	}
	if st.changed() > 0 {
		fmt.Fprintln(w)
	}
	for _, g := range []struct {
		mark  string
		paths []string
	}{{"M", st.modified}, {"A", st.added}, {"D", st.deleted}} {
		for _, p := range g.paths {
			fmt.Fprintf(w, "%s  %s\n", g.mark, p)
		}
	}
	return nil
}

func statusAll(root string, w io.Writer) error {
	names, err := list(root)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	src, err := walkFiles(root)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tMODIFIED\tADDED\tDELETED\tDISK")
	for _, name := range names {
		st, err := workspaceStatus(src, filepath.Join(root, spaceDir, name), cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\n", name, len(st.modified), len(st.added), len(st.deleted), humanSize(st.disk))
	}
	return tw.Flush()
}

// workspaceStatus compares a workspace with the source files (by size and mtime, which clones keep).
// Paths hidden by the [status] config are left out of the change lists but still count in disk.
func workspaceStatus(src map[string]fs.FileInfo, ws string, cfg config) (wsStatus, error) {
	var st wsStatus
	files, err := walkFiles(ws)
	if err != nil {
		return st, err
	}
	for rel, fi := range files {
		s, ok := src[rel]
		switch {
		case ignored(cfg.ignore, rel):
		case !ok:
			st.added = append(st.added, rel)
		case s.Size() != fi.Size() || !s.ModTime().Equal(fi.ModTime()):
			st.modified = append(st.modified, rel)
		}
		if fi.Mode().IsRegular() {
			n, err := privateSize(filepath.Join(ws, rel))
			if err != nil {
				return st, err
			}
			st.disk += n
		}
	}
	for rel := range src {
		if _, ok := files[rel]; !ok && !ignored(cfg.ignore, rel) {
			st.deleted = append(st.deleted, rel)
		}
	}
	if cfg.ignoreGitignored {
		gi, err := gitIgnored(ws, slices.Concat(st.modified, st.added, st.deleted))
		if err != nil {
			return st, err
		}
		hidden := func(rel string) bool { return gi[rel] }
		st.modified = slices.DeleteFunc(st.modified, hidden)
		st.added = slices.DeleteFunc(st.added, hidden)
		st.deleted = slices.DeleteFunc(st.deleted, hidden)
	}
	slices.Sort(st.modified)
	slices.Sort(st.added)
	slices.Sort(st.deleted)
	return st, nil
}

// walkFiles maps every non-directory entry under dir (relative path → info), skipping workleaf-space.
func walkFiles(dir string) (map[string]fs.FileInfo, error) {
	files := map[string]fs.FileInfo{}
	space := filepath.Join(dir, spaceDir)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == space {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files[rel(dir, path)] = fi
		return nil
	})
	return files, err
}

// privateSize returns the bytes of a file that APFS does not share with any clone
// (getattrlist ATTR_CMNEXT_PRIVATESIZE, which x/sys/unix does not wrap).
func privateSize(path string) (int64, error) {
	const attrCmnextPrivateSize = 0x8
	al := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Forkattr: attrCmnextPrivateSize}
	var buf [12]byte // u_int32_t length, then off_t private size
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	_, _, errno := unix.Syscall6(unix.SYS_GETATTRLIST,
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&al)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		unix.FSOPT_NOFOLLOW|unix.FSOPT_ATTR_CMN_EXTENDED, 0)
	if errno != 0 {
		return 0, fmt.Errorf("getattrlist %s: %w", path, errno)
	}
	return int64(binary.LittleEndian.Uint64(buf[4:])), nil
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f, unit := float64(n), -1
	for f >= 1024 && unit < 3 {
		f /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %cB", f, "KMGT"[unit])
}
