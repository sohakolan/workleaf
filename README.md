# Workleaf

**Full workspaces, without copying the whole project.**

Workleaf is a lightweight workspace manager, inspired by `git worktree`. It creates independent development environments from an existing project without physically duplicating its files.

> A full copy of the project, without really copying it.

## Intent

Creating an isolated copy of a large project (say 100,000 files, 5 GB) is slow and wastes disk space. Workleaf aims to make isolated workspaces:

- **Near-instant to create**: no file data is copied, whatever the project size.
- **Cheap on storage**: a workspace only uses extra space for what it actually modifies.
- **Fully isolated**: each workspace has its own changes. Workspaces don't affect each other or the source project.
- **Transparent**: workspaces are made of real files. Every tool (editor, git, npm, compilers…) works normally.
- **Safe**: nothing done inside a workspace can alter the source project.

## How it works

Workleaf relies on **Copy-on-Write (CoW)**, as provided natively by the file system:

1. **Create**: a workspace is created as a CoW clone of the project.
2. **Reference**: each file in the workspace is a new entry that points to the same data blocks as the original. No data is duplicated.
3. **Write**: when a file is modified in a workspace, the file system copies only the changed blocks.
4. **Isolate**: from then on, the change exists only in that workspace.

On macOS this uses APFS `clonefile(2)`, a kernel feature: no FUSE, no daemon, no kernel extension. Linux support (reflinks on btrfs/XFS, or overlayfs) is planned for later.

### Example

```
my-project/                 5 GB, 100,000 files
└── workleaf-space/
    ├── feature-a/          12 files modified → stores only those changes
    ├── feature-b/          35 files modified → stores only those changes
    └── experiment/          3 files modified → stores only those changes
```

Three independent environments, without three full copies of the project.

## Install

```sh
make           # builds bin/wl
make install   # builds and installs to /usr/local/bin (override with PREFIX=...)
```

## CLI

The command is `wl`.

```sh
wl init            # make the current directory a workleaf project (creates workleaf.conf)
wl create <name>   # create a workspace in <project>/workleaf-space/<name> (alias: wl add)
wl rm <name>       # run remove hooks, then delete the workspace (alias: wl remove)
wl ls              # list workspaces (alias: wl list)
wl status          # current workspace: files changed, real disk usage, git branch
                   # (outside a workspace: a summary of every workspace)
wl info            # current workspace in detail, with the list of changed files
wl completion zsh  # print a tab completion script (zsh, bash or fish)
```

### Tab completion

`wl <Tab>` completes commands and `wl rm <Tab>` completes workspace names. Open a new terminal after `make install`, which sets it up two ways:

- a completion file for each shell installed (zsh, bash, fish), in the directory that shell loads completions from;
- a block at the end of `~/.zshrc` and `~/.bashrc` (only those that exist), which also works when zsh's completion cache is stale (`compinit -C`):

```sh
# Workleaf start here
(( $+functions[compdef] )) && command -v wl >/dev/null && eval "$(wl completion zsh)"
# Workleaf end here
```

The markers keep it to a single block however many times you run `make install`, and let `make uninstall` remove it. `wl completion install` / `wl completion uninstall` do just that part.

```sh
sudo make uninstall   # removes the binary, the completion files and the rc blocks
```

`wl status` reports the disk space a workspace really uses: the bytes APFS does not share with any clone. Finder and `du` show the full logical size instead, which is misleading for clones.

Like git with `.git`, `wl` finds the project by walking up from the current directory to the nearest `workleaf.conf`, so it works from any subfolder. Inside a workspace, the cloned `workleaf.conf` is ignored: commands always target the main project.

Names can contain `/` (for example `feature/test` becomes `./workleaf-space/feature/test`).

Workspaces live inside the project, in `./workleaf-space/`. This folder is:

- skipped when cloning (otherwise the project would clone itself recursively);
- added to `.git/info/exclude` automatically (the project's `.gitignore` is not modified).

Each workspace gets its own CoW copy of `.git`, so it can switch branches independently of the source project.

## Configuration: `workleaf.conf`

`workleaf.conf` (created by `wl init`) marks the project root and defines hooks that run when a workspace is created or removed:

```ini
[create]
run = cp .env.example .env
run = npm install

[remove]
run = docker compose down
```

- Hooks run in the workspace directory, in order, with `sh -e`: a hook stops at its first failing command.
- They receive `WL_NAME`, `WL_PATH` and `WL_SOURCE` as environment variables.
- If a `create` hook fails, the workspace is deleted and the command fails.

For longer scripts, leave `run =` empty and indent the script below it. The whole block runs as one script, so variables are shared between its lines:

```ini
[create]
run =
  slug=$(echo "$WL_NAME" | tr / -)
  createdb "app-$slug"
  echo "DATABASE_URL=postgresql://localhost/app-$slug" >> .env.local
```

The block ends at the first line that is not indented. Indentation is removed, blank lines and `#` comments inside the block are kept.

The `[status]` section hides paths from the changes listed by `wl status` and `wl info`. Disk usage still counts everything.

```ini
[status]
# any file or folder named .git or .next, and its content
ignore = .git .next
# any file or folder name matching *.log
ignore = *.log
# a pattern with "/" matches from the project root
ignore = app/generated/*
# also hide what git ignores (tracked files are always shown)
ignore-gitignored = true
```

Several patterns can share a line, separated by spaces (so a pattern can't contain a space). Comments go on their own line (`#` or `;`).

`[status]` also accepts `run =` (one line or an indented block). Its output is shown by `wl status` and `wl info`, for example the URLs or database of the workspace:

```ini
[status]
run =
  echo "API       https://$(sed -n 's/^APP_HOST=//p' api/.env.local | tail -1)"
```

`wl init` writes `ignore = .git`, since git rewrites its own files on every branch switch or commit. The `.git` folder itself stays in the workspace: branches work as usual.

## Status

Early stage. The first version targets macOS (APFS) and is written in Go.

## Limits

- Workspaces must be on the same APFS volume as the project (always true with `./workleaf-space/`).
- Creation time grows with the **number** of files, not their size (a few seconds for ~100,000 files).
- Tools running on the source project (IDE indexing, test runners, watchers) may need to exclude `workleaf-space/`.
