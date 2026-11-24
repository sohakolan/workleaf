package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

const configFile = "workleaf.conf"

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

type config struct {
	hooks            map[string][]string // "create" / "remove" → commands
	ignore           []string            // patterns hidden from status change lists
	ignoreGitignored bool
}

// loadConfig reads workleaf.conf: [create] / [remove] sections with `run = <command>` lines
// (or `run =` followed by an indented multi-line script), and a [status] section with `ignore = <patterns...>` and `ignore-gitignored = true|false` lines.
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
	// A `run =` with no value starts a script made of the indented lines that follow.
	var script []string // nil when not inside a script
	scriptSection, scriptLine, indent, blanks := "", 0, "", 0
	endScript := func() error {
		if script == nil {
			return nil
		}
		if len(script) == 0 {
			return fmt.Errorf("%s:%d: expected an indented script after `run =`", configFile, scriptLine)
		}
		cfg.hooks[scriptSection] = append(cfg.hooks[scriptSection], strings.Join(script, "\n"))
		script = nil
		return nil
	}

	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		raw := sc.Text()
		if script != nil {
			body := strings.TrimLeft(raw, " \t")
			if body == "" {
				blanks++ // kept only if more script lines follow
				continue
			}
			if body != raw {
				if len(script) == 0 {
					indent = raw[:len(raw)-len(body)]
				}
				for ; blanks > 0; blanks-- {
					script = append(script, "")
				}
				script = append(script, strings.TrimPrefix(raw, indent))
				continue
			}
			if err := endScript(); err != nil {
				return cfg, err
			}
		}
		blanks = 0

		line := strings.TrimSpace(raw)
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
		case key == "run": // in [status], its output is shown by wl status and wl info
			if val == "" {
				script, scriptSection, scriptLine = []string{}, section, n
				continue
			}
			cfg.hooks[section] = append(cfg.hooks[section], val)
		case section != "status":
			return cfg, bad("`run = <command>`")
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
			return cfg, bad("`run = <command>`, `ignore = <pattern>` or `ignore-gitignored = true|false`")
		}
	}
	if err := sc.Err(); err != nil {
		return cfg, err
	}
	return cfg, endScript()
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

// runHooks runs each hook with `sh -e`, so a script stops at its first failing command.
func runHooks(cmds []string, root, ws, name string, out io.Writer) error {
	for _, c := range cmds {
		cmd := exec.Command("sh", "-ec", c)
		cmd.Dir = ws
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, os.Stderr
		cmd.Env = append(os.Environ(), "WL_NAME="+name, "WL_PATH="+ws, "WL_SOURCE="+root)
		if err := cmd.Run(); err != nil {
			first, _, multi := strings.Cut(c, "\n")
			if multi {
				first += " ..."
			}
			return fmt.Errorf("hook %q failed: %w", first, err)
		}
	}
	return nil
}
