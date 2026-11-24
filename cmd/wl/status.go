package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
)

// Status is bound by file-system calls, which APFS serves in parallel: 8 at once makes
// wl status about 10x faster on a 200k-file project, more barely helps.
const fsWorkers = 8

type wsStatus struct {
	modified, added, deleted []string // sorted paths relative to the workspace
	disk                     int64    // bytes not shared with the source or any other clone
}

func (st wsStatus) changed() int { return len(st.modified) + len(st.added) + len(st.deleted) }

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
	name := rel(filepath.Join(root, spaceDir), ws)
	fmt.Fprint(w, name)
	if b := gitBranch(ws); b != "" {
		fmt.Fprintf(w, " on branch %s", b)
	}
	fmt.Fprintf(w, "\n%d files changed, %s on disk\n", st.changed(), humanSize(st.disk))
	return runHooks(cfg.hooks["status"], root, ws, name, w)
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
	if err := runHooks(cfg.hooks["status"], root, ws, rel(filepath.Join(root, spaceDir), ws), w); err != nil {
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
	}
	if st.disk, err = diskUsage(ws, files); err != nil {
		return st, err
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
// Directories are read in parallel. Entries that vanish during the walk (caches of a running
// dev server) are skipped.
func walkFiles(dir string) (map[string]fs.FileInfo, error) {
	var (
		mu    sync.Mutex
		files = map[string]fs.FileInfo{}
		first error
		wg    sync.WaitGroup
		sem   = make(chan struct{}, fsWorkers)
	)
	space := filepath.Join(dir, spaceDir)
	var visit func(path string)
	visit = func(path string) {
		defer wg.Done()
		sem <- struct{}{}
		defer func() { <-sem }()
		found, err := readDir(path, space, func(sub string) { wg.Add(1); go visit(sub) })
		mu.Lock()
		defer mu.Unlock()
		if err != nil && first == nil {
			first = err
		}
		for p, fi := range found {
			files[rel(dir, p)] = fi
		}
	}
	wg.Add(1)
	visit(dir)
	wg.Wait()
	return files, first
}

// readDir returns the non-directory entries of one directory and hands each subdirectory
// (except skip) to sub.
func readDir(path, skip string, sub func(string)) (map[string]fs.FileInfo, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	found := map[string]fs.FileInfo{}
	for _, e := range entries {
		p := filepath.Join(path, e.Name())
		if e.IsDir() {
			if p != skip {
				sub(p)
			}
			continue
		}
		fi, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return found, err
		}
		found[p] = fi
	}
	return found, nil
}

// diskUsage sums the private size of the regular files of a workspace, in parallel.
func diskUsage(ws string, files map[string]fs.FileInfo) (int64, error) {
	var (
		total atomic.Int64
		wg    sync.WaitGroup
		once  sync.Once
		first error
		paths = make(chan string)
	)
	for range fsWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range paths {
				n, err := privateSize(p)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					once.Do(func() { first = err })
				}
				total.Add(n)
			}
		}()
	}
	for rel, fi := range files {
		if fi.Mode().IsRegular() {
			paths <- filepath.Join(ws, rel)
		}
	}
	close(paths)
	wg.Wait()
	return total.Load(), first
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
