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
	"text/tabwriter"
)

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
