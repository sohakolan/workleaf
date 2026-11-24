package main

import (
	"os"
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
