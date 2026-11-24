# AGENTS.md

Guidance for AI agents working on Workleaf. Read `README.md` first for the project's intent.

## Project in one line

`wl` is a Go CLI that creates isolated workspaces of a project in `./workleaf-space/<name>` using APFS copy-on-write clones, with optional hooks from `workleaf.conf`.

## Decisions already made (don't revisit without asking)

- **Name**: Workleaf. CLI binary: `wl`. Config file: `workleaf.conf`. Workspace folder: `workleaf-space/`.
- **Language**: Go, standard library first. No dependency for something a few lines can do.
- **Platform**: macOS (APFS) only for now. Linux comes later; don't add abstractions for it yet.
- **CoW mechanism**: APFS `clonefile(2)`. No FUSE, macFUSE, FUSE-T, FSKit or daemon. Workspaces are made of real files.
- **Location**: workspaces live inside the project, in `./workleaf-space/<name>`. Names may contain `/`.
- **Project root**: `wl init` creates `workleaf.conf`, which marks the root. Other commands walk up to the nearest `workleaf.conf`, like git with `.git`, skipping the cloned copies inside workspaces.

## Invariants (must always hold)

1. **The source project is never modified** by workspace operations, except for adding `workleaf-space/` to `.git/info/exclude`.
2. **`workleaf-space/` is never cloned.** Clone the project's top-level entries one by one and skip it, or the project would clone itself recursively.
3. **No file data is copied** on create. If cloning fails (for example on a non-APFS volume), fail with a clear error. Never fall back to a regular copy silently.
4. **A failed `create` leaves nothing behind**: if cloning or a `create` hook fails, delete the partial workspace.
5. **`rm` only deletes inside `workleaf-space/`.** Validate names: no `..`, no absolute paths, no empty segments.

## Hooks (`workleaf.conf`)

- INI-like format: `[create]` and `[remove]` sections, one `run = <command>` per line, executed in order.
- Each command runs with `sh -c`, with the workspace as working directory.
- Environment: inherited, plus `WL_NAME`, `WL_PATH` (absolute workspace path) and `WL_SOURCE` (absolute project path).
- Stop at the first failing hook.

## Working conventions

- Keep it simple: minimum code that solves the problem, no speculative features or options.
- Every behavior that touches the file system gets a test, using a temporary directory. The key test: modifying a file in a workspace leaves the source unchanged.
- Run `go vet ./...` and `go test ./...` before considering a change done.
- Docs and code in English.

## Commits

Workflow:

1. Analyze the changes and propose a plan of atomic commits (message, reason, files). Wait for approval.
2. Then, one commit at a time:
   1. Check the index is clean (`git status --short`).
   2. Stage only the current commit (`git add <files>`).
   3. Check the index matches the announced commit exactly (`git diff --cached --stat`, `git status --short`).
   4. Commit: `git commit -m "type(scope): description"`.
3. When a file carries two intentions, split it with a patch applied via `git apply --cached` (`git add -p` is interactive and unusable). If the patch doesn't apply cleanly, let the user stage it.

Message format (mandatory): `<type>(<scope>): <description>`

- Types: `feat`, `fix`, `chore`, `refactor`. No other type.
- Scopes (mandatory): `front`, `back`, `docs`. In this project, the Go CLI is `back`.
- The description is always in English.
- Never mix intentions in one commit. Keep commits as small as possible while coherent.

The agent is allowed to run: `git status`, `git diff`, `git log`, `git show`, `git add <files>`, `git apply --cached`, `git restore --staged <files>`, `git commit`, `git push`.

The agent must never run: `git pull`, `git merge`, `git rebase`, `git reset --hard`, `git checkout .`, `git restore` on the working tree, `git stash`, or create/switch/delete a branch. It never stages more than one commit at a time.
