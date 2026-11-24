package main

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

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
