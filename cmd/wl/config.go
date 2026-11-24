package main

import (
	"bufio"
	"errors"
	"fmt"
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
