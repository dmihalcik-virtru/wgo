---
ticket: WGO-112
title: wgo join — add a repo to an existing multi-repo workspace
status: draft
authors: [dmihalcik]
branches: [virtru/wgo:WGO-112-wgo-join]
prs: []
created: 2026-05-08
updated: 2026-05-08
phase: 2
estimate: 1d
depends_on: [WGO-101]
---

# WGO-112 — `wgo join`: add a repo to an existing multi-repo workspace

## Summary

Add `wgo join <owner/repo>` to bring an additional repository into an existing multi-repo workspace on the same branch. Enhance `wgo .` to show sibling repos when run inside a genuine multi-repo workspace.

## Problem / Motivation

`wgo add TICKET desc -r repo1 -r repo2` creates a shared root directory with sibling worktrees, one per repo. But the set of repos isn't always known upfront:

- A feature that starts in one repo reveals it needs changes in a second repo mid-implementation.
- `wgo to owner/repo@branch` checks out a single repo; there is no follow-on command to pull a sibling repo into the same directory structure.

Without `wgo join`, the developer must manually clone/fetch/worktree-add the second repo, then hand-update plan.md and state.json to record the association. `wgo .` offers no signal that sibling repos exist alongside the current one.

## Proposed Solution

1. **New command `wgo join <owner/repo>`** — detects the current worktree's branch and shared root, creates a sibling worktree for the new repo on the same branch, and updates plan.md and state.json with the same annotation.
2. **`wgo .` sibling display** — when the parent directory of the current worktree contains 2 or more git repos, print a "Workspace siblings" section listing the non-current siblings with their branch and status.

## Inputs / Outputs / Contracts

### `wgo join <owner/repo>`

```
wgo join virtru/cli
cd $(wgo join virtru/cli)      # stdout is the new worktree path
```

**Flags:**
- `--no-push` — skip pushing when a new branch is created (default: push, matching `wgo add`)

**Algorithm:**
1. `git rev-parse --show-toplevel` → `currentWtPath`
2. `gitClient.CurrentBranch(currentWtPath)` → `branch`
3. `filepath.Dir(currentWtPath)` → `sharedRoot`
4. Parse `owner/repo` arg with `parseRepoSpecs` (reuse from `add.go`)
5. `findOrCloneRepo(gitClient, cfg, owner, repo)` (reuse from `to.go`)
6. `gitClient.Fetch(repoPath)` — best-effort, warn on failure
7. `newWtPath = filepath.Join(sharedRoot, repo)`
8. Error if `newWtPath` already exists as a directory
9. `gitClient.BranchExists(repoPath, branch)`:
   - **true** → `gitClient.WorktreeAdd(repoPath, newWtPath, branch, false, "")` — check out existing branch
   - **false** → `gitClient.WorktreeAdd(repoPath, newWtPath, branch, true, "origin/"+defaultBranch)` + push branch — create new branch from default
10. Look up `state.annotations[currentWtPath+":"+branch]` to get the existing `purpose`/reason; fall back to branch name if not found
11. `p.AddBranch(repo, branch, reason, "")` — update plan.md
12. `state.AddAnnotation(newWtPath, branch, reason)` + `state.AddRepo(newWtPath, "")` — update state.json
13. `fmt.Println(newWtPath)` — stdout only; all logging to stderr

**Files touched:**
- `internal/cmd/join.go` — new file
- `internal/plan/plan.go` — reuse `AddBranch`
- `internal/store/state.go` — reuse `AddAnnotation`, `AddRepo`
- `internal/store/store.go` — reuse `LoadState`, `SaveState`, `LoadPlan`, `SavePlan`
- `internal/git/` — reuse `BranchExists`, `WorktreeAdd`, `Fetch`, `CurrentBranch`, `DefaultBranch`, `Push`
- `internal/cmd/add.go` — reuse `findOrCloneRepo`, `parseRepoSpecs` (these are package-level functions, available within `cmd` package)

### `wgo .` sibling display (enhancement)

When `wgo .` runs inside a git worktree:
1. Get `parentDir = filepath.Dir(wtPath)`
2. Scan immediate subdirs of `parentDir`; call `gitClient.IsRepo(subdir)` for each
3. If the count of git-repo subdirs is ≥ 2 (meaning at least one sibling besides the current dir exists), print a "Workspace siblings:" section:

```
Branch:   WGO-112-wgo-join
Status:   1 modified
Spec:     📄 spec/WGO-112.md (draft)
...

Workspace siblings:
  cli/      WGO-112-wgo-join   clean
  infra/    WGO-112-wgo-join   3 modified
```

- Only scan immediate children (no recursion)
- Show branch and dirty/clean status for each sibling
- Skip the section entirely if parent has fewer than 2 git repos (avoids false positives from repo root being a parent of only one workspace)

**Files touched:**
- `internal/cmd/dot.go` — add sibling detection after existing output

## Edge Cases & Constraints

- **`newWtPath` already exists**: error with message `worktree already exists at <path>; remove it first or use cd <path>`.
- **Branch doesn't exist on new repo**: create from `origin/<defaultBranch>` and push. If push fails: remove the newly created worktree (rollback), propagate error.
- **Not in a git repo**: `git rev-parse --show-toplevel` fails → clear error, no action.
- **`sharedRoot` is the worktrees_dir itself** (i.e., the current worktree is not nested one level deep): still works; sibling lands adjacent. `wgo .` sibling scan may produce false positives in this case if other unrelated repos are siblings — acceptable; user is unlikely to put `join`-created repos directly in worktrees_dir.
- **`gitClient.IsRepo` in sibling scan**: use `gitClient.IsRepo(path)` which already exists; skip symlinks and non-directories.
- **Performance of sibling scan**: max one directory listing + N `IsRepo` calls (each is `git rev-parse` in a subdir, ~2ms each). Cap at 10 siblings to bound latency. If > 10 subdirs are git repos, show the first 10 alphabetically with a `(…and N more)` note.
- **`wgo .` from a non-worktree repo (e.g. main clone)**: `filepath.Dir(wtPath)` is the parent of the main clone, which is unlikely to be a multi-repo workspace root. Sibling scan runs but typically finds 0 or 1 git repos → section suppressed. No behavior change.
- **Config not initialized**: `wgo join` requires `worktree.worktrees_dir` for `findOrCloneRepo` cloning. If config is missing, error before any git operations.
- **Annotation lookup failure**: if `state.json` has no annotation for the current worktree:branch, fall back to `branch` as the reason string. Do not error.

## Out of Scope

- Removing a repo from a workspace (`wgo leave` or similar).
- Showing sibling repos in `wgo status` as a grouped workspace unit (status groups by main repo, not shared root).
- Spec scaffolding for the joined repo (the spec already lives in the primary repo; `join` does not create a second spec).
- Multi-worktree join (adding >1 repo in a single invocation — user can run `wgo join` twice).
- Push behavior for repos where the branch already exists remotely (no push needed; existing remote branch is checked out).

## Acceptance Criteria

- [ ] `wgo join virtru/cli` from inside a worktree creates `../cli/` on the same branch, prints the path to stdout, exits 0
- [ ] Plan.md gains a new branch entry `cli:<branch>` with the same reason as the current worktree's annotation
- [ ] `~/.wgo/state.json` gains a new annotation for `<newWtPath>:<branch>` and a new repo entry for `newWtPath`
- [ ] When the branch does not exist on the joined repo, `wgo join` creates it from `origin/<default>` and pushes it; `--no-push` skips the push
- [ ] When the branch already exists on the joined repo, no push is attempted
- [ ] `wgo join` from a directory that is not a git repo exits non-zero with a clear error
- [ ] `wgo join` when `newWtPath` already exists exits non-zero with a clear error (no partial state written)
- [ ] `wgo .` from inside a worktree whose parent contains 2+ git repos shows a "Workspace siblings:" section listing the non-current siblings with branch and clean/dirty status
- [ ] `wgo .` from inside a worktree whose parent has only 1 git repo (itself) does not show the siblings section
- [ ] `cd $(wgo join virtru/cli)` works as a shell idiom (stdout is only the path, no extra output)
