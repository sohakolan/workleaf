package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStatus(t *testing.T) {
	root := project(t, "[status]\nignore = .git\n")
	cfg := config{ignore: []string{".git"}}
	if err := create(root, "w"); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, spaceDir, "w")
	src, err := walkFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := workspaceStatus(src, ws, cfg); err != nil || st.changed() != 0 || st.disk != 0 {
		t.Fatalf("fresh workspace status = %+v, %v (want all zero: clones share every block)", st, err)
	}

	write(t, filepath.Join(ws, "a.txt"), "changed content")
	write(t, filepath.Join(ws, "new.bin"), strings.Repeat("x", 100<<10))
	if err := os.Remove(filepath.Join(ws, "src", "b.txt")); err != nil {
		t.Fatal(err)
	}
	// Files matching an ignore pattern (here .git) are not reported as changes.
	write(t, filepath.Join(ws, ".git", "HEAD"), "ref: refs/heads/other-branch\n")
	write(t, filepath.Join(ws, ".git", "index"), "new")
	st, err := workspaceStatus(src, ws, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.modified, []string{"a.txt"}) || !reflect.DeepEqual(st.added, []string{"new.bin"}) ||
		!reflect.DeepEqual(st.deleted, []string{"src/b.txt"}) || st.disk < 100<<10 {
		t.Fatalf("status = %+v", st)
	}

	// Outside a workspace: one summary line per workspace.
	var out strings.Builder
	if err := status(root, root, &out); err != nil || !strings.Contains(out.String(), "w     1         1      1") {
		t.Fatalf("status output:\n%s(err %v)", out.String(), err)
	}

	// Inside a workspace (from a subfolder): just that workspace.
	inWs := filepath.Join(ws, "src")
	out.Reset()
	if err := status(root, inWs, &out); err != nil || !strings.HasPrefix(out.String(), "w\n3 files changed, ") {
		t.Fatalf("status in workspace:\n%s(err %v)", out.String(), err)
	}
	out.Reset()
	if err := info(root, inWs, &out); err != nil || !strings.Contains(out.String(), "M  a.txt\nA  new.bin\nD  src/b.txt\n") {
		t.Fatalf("info:\n%s(err %v)", out.String(), err)
	}
	if err := info(root, root, &out); err == nil {
		t.Fatal("info outside a workspace succeeded")
	}
}

func TestGitBranch(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "-b", "feat/x", root).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	if err := initProject(root); err != nil {
		t.Fatal(err)
	}
	if err := create(root, "w"); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, spaceDir, "w")
	if got := gitBranch(ws); got != "feat/x" {
		t.Fatalf("gitBranch = %q", got)
	}
	var out strings.Builder
	if err := status(root, ws, &out); err != nil || !strings.HasPrefix(out.String(), "w on branch feat/x\n") {
		t.Fatalf("status:\n%s(err %v)", out.String(), err)
	}
	if got := gitBranch(t.TempDir()); got != "" {
		t.Fatalf("gitBranch without git = %q", got)
	}
}

func TestStatusRunShowsOutput(t *testing.T) {
	root := project(t, "[status]\nrun =\n  echo \"API  https://api-$WL_NAME.test\"\n")
	if err := create(root, "w"); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, spaceDir, "w")
	var out strings.Builder
	if err := status(root, ws, &out); err != nil || !strings.HasSuffix(out.String(), "on disk\nAPI  https://api-w.test\n") {
		t.Fatalf("status:\n%s(err %v)", out.String(), err)
	}
	out.Reset()
	if err := info(root, ws, &out); err != nil || !strings.Contains(out.String(), "\nAPI  https://api-w.test\n") {
		t.Fatalf("info:\n%s(err %v)", out.String(), err)
	}
}
