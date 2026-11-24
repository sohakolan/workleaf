// Command wl creates isolated copy-on-write workspaces of a project.
package main

import (
	"errors"
	"fmt"
	"os"
)

const usage = `usage:
  wl init            make the current directory a workleaf project (creates workleaf.conf)
  wl create <name>   create a workspace in <project>/workleaf-space/<name>
  wl rm <name>       run remove hooks, then delete the workspace
  wl ls              list workspaces
  wl status          current workspace: changed files, real disk usage, git branch
                     (outside a workspace: a summary of every workspace)
  wl info            current workspace in detail, with the list of changed files`

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
	root, err := findRoot(cwd)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 2 && args[0] == "create":
		return create(root, args[1])
	case len(args) == 2 && args[0] == "rm":
		return remove(root, args[1])
	case len(args) == 1 && args[0] == "ls":
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
