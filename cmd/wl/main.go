// Command wl creates isolated copy-on-write workspaces of a project.
package main

import (
	"errors"
	"fmt"
	"os"
)

const usage = `usage:
  wl init            make the current directory a workleaf project (creates workleaf.conf)
  wl create <name>   create a workspace in <project>/workleaf-space/<name> (alias: add)
  wl rm <name>       run remove hooks, then delete the workspace (alias: remove)
  wl ls              list workspaces (alias: list)
  wl status          current workspace: changed files, real disk usage, git branch
                     (outside a workspace: a summary of every workspace)
  wl info            current workspace in detail, with the list of changed files
  wl completion <zsh|bash|fish>  print a tab completion script (make install sets it up)
  wl completion <install|uninstall>  add/remove the completion block in ~/.zshrc and ~/.bashrc`

func main() {
	cwd, err := os.Getwd()
	if err == nil {
		err = run(cwd, os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wl:", err)
		os.Exit(1)
	}
}

func run(cwd string, args []string) error {
	if len(args) == 1 && args[0] == "init" {
		return initProject(cwd)
	}
	if len(args) == 2 && args[0] == "completion" {
		return completion(args[1])
	}
	root, err := findRoot(cwd)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 2 && (args[0] == "create" || args[0] == "add"):
		return create(root, args[1])
	case len(args) == 2 && (args[0] == "rm" || args[0] == "remove"):
		return remove(root, args[1])
	case len(args) == 1 && (args[0] == "ls" || args[0] == "list"):
		names, err := list(root)
		for _, n := range names {
			fmt.Println(n)
		}
		return err
	case len(args) == 1 && args[0] == "status":
		return status(root, cwd, os.Stdout)
	case len(args) == 1 && args[0] == "info":
		return info(root, cwd, os.Stdout)
	}
	return errors.New(usage)
}
