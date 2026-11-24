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
wl create <name>   # create a workspace in <project>/workleaf-space/<name>
wl rm <name>       # run remove hooks, then delete the workspace
wl ls              # list workspaces
wl status          # current workspace: files changed, real disk usage, git branch
                   # (outside a workspace: a summary of every workspace)
wl info            # current workspace in detail, with the list of changed files
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

- Hooks run in the workspace directory, in order.
- They receive `WL_NAME`, `WL_PATH` and `WL_SOURCE` as environment variables.
- If a `create` hook fails, the workspace is deleted and the command fails.

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

`wl init` writes `ignore = .git`, since git rewrites its own files on every branch switch or commit. The `.git` folder itself stays in the workspace: branches work as usual.

## Status

Early stage. The first version targets macOS (APFS) and is written in Go.

## Limits

- Workspaces must be on the same APFS volume as the project (always true with `./workleaf-space/`).
- Creation time grows with the **number** of files, not their size (a few seconds for ~100,000 files).
- Tools running on the source project (IDE indexing, test runners, watchers) may need to exclude `workleaf-space/`.
