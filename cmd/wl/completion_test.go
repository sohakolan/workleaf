package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithBlock(t *testing.T) {
	orig := "export A=1\nsource x\n"
	added, err := withBlock(orig, "eval v1")
	if err != nil || added != orig+"\n"+rcStart+"\neval v1\n"+rcEnd+"\n" {
		t.Fatalf("add = %q, %v", added, err)
	}
	again, _ := withBlock(added, "eval v1")
	if again != added {
		t.Fatalf("second add changed the file:\n%q", again)
	}
	updated, _ := withBlock(added+"export B=2\n", "eval v2")
	if strings.Count(updated, rcStart) != 1 || !strings.Contains(updated, "\neval v2\n") || !strings.HasSuffix(updated, rcEnd+"\nexport B=2\n") {
		t.Fatalf("update = %q", updated)
	}
	removed, _ := withBlock(added, "")
	if removed != orig {
		t.Fatalf("remove = %q, want the original back", removed)
	}
	if noNewline, _ := withBlock("export A=1", "eval v1"); !strings.HasPrefix(noNewline, "export A=1\n\n"+rcStart) {
		t.Fatalf("add after a last line without newline = %q", noNewline)
	}
	if _, err := withBlock(rcStart+"\neval v1\n", "eval v2"); err == nil {
		t.Fatal("start marker without end marker accepted")
	}
}

func TestSetupRC(t *testing.T) {
	home := t.TempDir()
	zshrc := filepath.Join(home, ".zshrc")
	orig := "autoload -Uz compinit && compinit -C\n"
	write(t, zshrc, orig)

	for range 3 { // make install run several times: still one block
		if err := setupRC(home, false, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	got := read(t, zshrc)
	if strings.Count(got, rcStart) != 1 || !strings.Contains(got, `eval "$(wl completion zsh)"`) {
		t.Fatalf(".zshrc =\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); err == nil {
		t.Fatal("created a .bashrc that didn't exist")
	}

	if err := setupRC(home, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := read(t, zshrc); got != orig {
		t.Fatalf("after uninstall .zshrc =\n%q", got)
	}
}
