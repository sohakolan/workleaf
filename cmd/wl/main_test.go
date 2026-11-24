package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func project(t *testing.T, conf string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "original")
	write(t, filepath.Join(root, "src", "b.txt"), "nested")
	write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	if conf != "" {
		write(t, filepath.Join(root, configFile), conf)
	}
	return root
}

func TestCreateIsolatesSource(t *testing.T) {
	root := project(t, "[create]\nrun = echo \"$WL_NAME\" > hook.txt\n")
	if err := create(root, "feature/test"); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, spaceDir, "feature", "test")

	if got := read(t, filepath.Join(ws, "src", "b.txt")); got != "nested" {
		t.Fatalf("cloned file = %q", got)
	}
	write(t, filepath.Join(ws, "a.txt"), "changed")
	write(t, filepath.Join(ws, "src", "b.txt"), "changed")
	if read(t, filepath.Join(root, "a.txt")) != "original" || read(t, filepath.Join(root, "src", "b.txt")) != "nested" {
		t.Fatal("writing in the workspace changed the source")
	}
	if got := read(t, filepath.Join(ws, "hook.txt")); got != "feature/test\n" {
		t.Fatalf("hook output = %q", got)
	}
	if _, err := os.Stat(filepath.Join(ws, spaceDir)); err == nil {
		t.Fatal("workleaf-space was cloned into the workspace")
	}
	if !strings.Contains(read(t, filepath.Join(root, ".git", "info", "exclude")), "/workleaf-space/\n") {
		t.Fatal("workleaf-space not excluded from git")
	}

	// A second workspace must not contain the first one, and the exclude line is not duplicated.
	if err := create(root, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, spaceDir, "other", spaceDir)); err == nil {
		t.Fatal("second workspace contains workleaf-space")
	}
	if n := strings.Count(read(t, filepath.Join(root, ".git", "info", "exclude")), "/workleaf-space/"); n != 1 {
		t.Fatalf("exclude line written %d times", n)
	}

	names, err := list(root)
	if err != nil || !reflect.DeepEqual(names, []string{"feature/test", "other"}) {
		t.Fatalf("list = %v, %v", names, err)
	}
}

func TestCreateFailingHookLeavesNothing(t *testing.T) {
	root := project(t, "[create]\nrun = exit 3\n")
	if err := create(root, "feature/x"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(filepath.Join(root, spaceDir, "feature")); err == nil {
		t.Fatal("partial workspace left behind")
	}
}

func TestCreateRejects(t *testing.T) {
	root := project(t, "")
	if err := create(root, "a"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "/abs", "../out", "x/../../out", "x//y", "./x", "x/", "a", "a/inner"} {
		if err := create(root, name); err == nil {
			t.Errorf("create(%q) succeeded", name)
		}
	}
}

func TestRemove(t *testing.T) {
	root := project(t, "[remove]\nrun = touch \"$WL_SOURCE/removed-hook\"\n")
	if err := create(root, "feature/test"); err != nil {
		t.Fatal(err)
	}
	if err := remove(root, "feature"); err == nil {
		t.Fatal("removed an intermediate folder")
	}
	if err := remove(root, "feature/test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "removed-hook")); err != nil {
		t.Fatal("remove hook did not run")
	}
	if _, err := os.Stat(filepath.Join(root, spaceDir, "feature")); err == nil {
		t.Fatal("empty parent folder left behind")
	}
	if read(t, filepath.Join(root, "a.txt")) != "original" {
		t.Fatal("source changed")
	}
}

func TestRemoveFailingHookKeepsWorkspace(t *testing.T) {
	root := project(t, "[remove]\nrun = false\n")
	if err := create(root, "w"); err != nil {
		t.Fatal(err)
	}
	if err := remove(root, "w"); err == nil {
		t.Fatal("expected error")
	}
	if !isWorkspace(filepath.Join(root, spaceDir, "w")) {
		t.Fatal("workspace deleted despite failing hook")
	}
}

func TestLoadConfigRejectsBadLines(t *testing.T) {
	for _, conf := range []string{
		"run = x\n", "[create]\nfoo = x\n", "[create]\nrun =\n", "[other]\n", "[create]\nignore = x\n",
		"[status]\nrun = x\n", "[status]\nignore =\n", "[status]\nignore = [\n", "[status]\nignore = .git [\n", "[status]\nignore-gitignored = yes\n",
	} {
		if _, err := loadConfig(project(t, conf)); err == nil {
			t.Errorf("loadConfig(%q) succeeded", conf)
		}
	}
}

func TestLoadConfigIgnoreSeveralPerLine(t *testing.T) {
	cfg, err := loadConfig(project(t, "[status]\nignore = .git  .next\nignore = app/gen/ *.log\n"))
	if err != nil || !reflect.DeepEqual(cfg.ignore, []string{".git", ".next", "app/gen", "*.log"}) {
		t.Fatalf("ignore = %v, %v", cfg.ignore, err)
	}
}

func TestIgnored(t *testing.T) {
	patterns := []string{".git", "*.log", "app/generated/*"}
	for rel, want := range map[string]bool{
		".git/index":              true,
		"sub/.git/HEAD":           true,
		"debug.log":               true,
		"logs/x/debug.log":        true,
		"app/generated/a.ts":      true,
		"app/generated/deep/b":    true,
		"other/app/generated/a":   false,
		"app/page.tsx":            false,
		".github/workflows/x.yml": false,
	} {
		if got := ignored(patterns, rel); got != want {
			t.Errorf("ignored(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestInitAndFindRoot(t *testing.T) {
	root := project(t, "")
	sub := filepath.Join(root, "src")
	if _, err := findRoot(sub); err == nil {
		t.Fatal("found a root before init")
	}
	if err := initProject(root); err != nil {
		t.Fatal(err)
	}
	if err := initProject(root); err == nil {
		t.Fatal("init overwrote an existing workleaf.conf")
	}
	if cfg, err := loadConfig(root); err != nil || len(cfg.hooks) != 0 || !reflect.DeepEqual(cfg.ignore, []string{".git"}) || cfg.ignoreGitignored {
		t.Fatalf("template config = %+v, %v", cfg, err)
	}
	if got, err := findRoot(sub); err != nil || got != root {
		t.Fatalf("findRoot(sub) = %q, %v", got, err)
	}

	// From inside a workspace, the cloned workleaf.conf is skipped: the main project wins.
	if err := create(root, "feature/test"); err != nil {
		t.Fatal(err)
	}
	inWs := filepath.Join(root, spaceDir, "feature", "test", "src")
	if got, err := findRoot(inWs); err != nil || got != root {
		t.Fatalf("findRoot(in workspace) = %q, %v", got, err)
	}
	if err := run(inWs, []string{"create", "other"}); err != nil {
		t.Fatal(err)
	}
	if !isWorkspace(filepath.Join(root, spaceDir, "other")) {
		t.Fatal("create from inside a workspace did not target the main project")
	}
}

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

func TestIgnoreGitignored(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	write(t, filepath.Join(root, ".gitignore"), "*.tmp\n")
	write(t, filepath.Join(root, "tracked.tmp"), "v1")
	if out, err := exec.Command("git", "-C", root, "add", "-f", "tracked.tmp").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if err := create(root, "w"); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, spaceDir, "w")
	write(t, filepath.Join(ws, "cache.tmp"), "x")
	write(t, filepath.Join(ws, "code.go"), "x")
	write(t, filepath.Join(ws, "tracked.tmp"), "v2 changed")

	src, err := walkFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := workspaceStatus(src, ws, config{ignore: []string{".git"}, ignoreGitignored: true})
	if err != nil {
		t.Fatal(err)
	}
	// cache.tmp is gitignored; tracked.tmp matches *.tmp but is tracked, so it stays.
	if !reflect.DeepEqual(st.added, []string{"code.go"}) || !reflect.DeepEqual(st.modified, []string{"tracked.tmp"}) {
		t.Fatalf("status = %+v", st)
	}
}
