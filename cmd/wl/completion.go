package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// completions are printed by `wl completion <shell>`; make install puts them where each shell loads them.
var completions = map[string]string{
	"zsh": `#compdef wl
_wl() {
  local -a commands workspaces
  commands=(
    'init:make the current directory a workleaf project'
    'create:create a workspace'
    'add:create a workspace (alias of create)'
    'rm:run remove hooks, then delete a workspace'
    'remove:delete a workspace (alias of rm)'
    'ls:list workspaces'
    'list:list workspaces (alias of ls)'
    'status:current workspace summary'
    'info:current workspace in detail'
    'completion:print a shell completion script'
  )
  if (( CURRENT == 2 )); then
    _describe 'command' commands
  elif (( CURRENT == 3 )); then
    case $words[2] in
      rm|remove)
        workspaces=(${(f)"$(wl ls 2>/dev/null)"})
        compadd -a workspaces
        ;;
      completion) compadd zsh bash fish ;;
    esac
  fi
}
# Works both autoloaded from fpath and loaded with eval "$(wl completion zsh)".
if [ "$funcstack[1]" = "_wl" ]; then _wl "$@"; else compdef _wl wl; fi
`,
	"bash": `_wl() {
  local cur=${COMP_WORDS[COMP_CWORD]} IFS=$'\n'
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=($(compgen -W "$(printf '%s\n' init create add rm remove ls list status info completion)" -- "$cur"))
  elif [ "$COMP_CWORD" -eq 2 ]; then
    case ${COMP_WORDS[1]} in
      rm|remove) COMPREPLY=($(compgen -W "$(wl ls 2>/dev/null)" -- "$cur")) ;;
      completion) COMPREPLY=($(compgen -W "$(printf '%s\n' zsh bash fish)" -- "$cur")) ;;
    esac
  fi
}
complete -F _wl wl
`,
	"fish": `complete -c wl -f
complete -c wl -n __fish_use_subcommand -a init -d 'make the current directory a workleaf project'
complete -c wl -n __fish_use_subcommand -a 'create add' -d 'create a workspace'
complete -c wl -n __fish_use_subcommand -a 'rm remove' -d 'delete a workspace'
complete -c wl -n __fish_use_subcommand -a 'ls list' -d 'list workspaces'
complete -c wl -n __fish_use_subcommand -a status -d 'current workspace summary'
complete -c wl -n __fish_use_subcommand -a info -d 'current workspace in detail'
complete -c wl -n __fish_use_subcommand -a completion -d 'print a shell completion script'
complete -c wl -n '__fish_seen_subcommand_from rm remove' -a '(wl ls 2>/dev/null)'
complete -c wl -n '__fish_seen_subcommand_from completion' -a 'zsh bash fish'
`,
}

func completion(arg string) error {
	if arg == "install" || arg == "uninstall" {
		home, err := homeDir()
		if err != nil {
			return err
		}
		return setupRC(home, arg == "uninstall", os.Stdout)
	}
	script, ok := completions[arg]
	if !ok {
		return fmt.Errorf("unknown shell %q: use zsh, bash or fish (or install / uninstall)", arg)
	}
	fmt.Print(script)
	return nil
}

// Shell rc files get a block between these markers, so it is added once, kept up to date
// and removed cleanly. Loading completion from the rc file works even when compinit uses a
// stale cache (compinit -C), which ignores the completion file installed in fpath.
const (
	rcStart = "# Workleaf start here"
	rcEnd   = "# Workleaf end here"
)

var rcBlocks = []struct{ file, body string }{
	{".zshrc", `(( $+functions[compdef] )) && command -v wl >/dev/null && eval "$(wl completion zsh)"`},
	{".bashrc", `command -v wl >/dev/null && eval "$(wl completion bash)"`},
}

// setupRC adds (or with remove, deletes) the workleaf block in the rc files that exist in home.
func setupRC(home string, remove bool, out io.Writer) error {
	for _, rc := range rcBlocks {
		path := filepath.Join(home, rc.file)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // that shell isn't set up: leave it alone
		}
		if err != nil {
			return err
		}
		body := rc.body
		if remove {
			body = ""
		}
		updated, err := withBlock(string(data), body)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if updated == string(data) {
			continue
		}
		// WriteFile on an existing file keeps its owner and mode, even when run with sudo.
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "updated %s\n", path)
	}
	return nil
}

// withBlock returns content with the marked block set to body: replaced if present,
// appended otherwise, removed when body is empty.
func withBlock(content, body string) (string, error) {
	block := ""
	if body != "" {
		block = rcStart + "\n" + body + "\n" + rcEnd + "\n"
	}
	start := strings.Index(content, rcStart+"\n")
	if start < 0 {
		if block == "" {
			return content, nil
		}
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + "\n" + block, nil
	}
	end := strings.Index(content[start:], rcEnd)
	if end < 0 {
		return "", fmt.Errorf("%q without %q, fix it by hand", rcStart, rcEnd)
	}
	before, after := content[:start], content[start+end+len(rcEnd):]
	after = strings.TrimPrefix(after, "\n")
	if block == "" && strings.HasSuffix(before, "\n\n") {
		before = before[:len(before)-1] // drop the blank line added with the block
	}
	return before + block + after, nil
}

// homeDir is the invoking user's home, also under sudo (make install).
func homeDir() (string, error) {
	if name := os.Getenv("SUDO_USER"); name != "" {
		u, err := user.Lookup(name)
		if err != nil {
			return "", err
		}
		return u.HomeDir, nil
	}
	return os.UserHomeDir()
}
