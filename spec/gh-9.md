---
ticket: gh-9
title: Stacked / related worktrees — keep them in sync
status: draft
authors: [dmihalcik]
branches: [virtru/wgo:gh-9-stacked-prs]
prs: []
issue: https://github.com/dmihalcik-virtru/wgo/issues/9
created: 2026-05-22
updated: 2026-05-22
phase: 1
estimate: 3d
depends_on: []
---

# gh-9 — Stacked / related worktrees: DAG-aware tracking and restack

## Summary

Add first-class support for stacked pull requests to `wgo`: track parent/child relationships between branches as a DAG, restack downstream branches across worktrees when an upstream parent changes, and keep GitHub PR bases correct as parents land. Source of truth is local `~/.wgo/state.json`; a fenced `<!-- wgo-stack:<id> -->` block is mirrored into each PR body so reviewers see the topology and another machine can rebuild local state.

## Problem / Motivation

`wgo` currently treats branches as flat, independent entities. As soon as a developer splits work into a chain of small, reviewable PRs (bottom-layer refactor → middle plumbing → top feature), `wgo` can't help:

- `wgo .` doesn't tell you you're standing on layer 2 of 3.
- `wgo clean` will happily offer to delete a "merged" parent whose child PR still depends on it.
- `wgo to` always bases new branches on `origin/<default>`; there's no way to base on another in-flight branch.
- There is no rebase/restack flow at all — and `git rebase --update-refs`, the obvious tool, doesn't coordinate across worktrees, which is precisely the environment `wgo` targets (one worktree per active branch).

Issue #9 calls this out concretely: a large stack of PRs on `opentdf/tests` is hard to keep in sync while fixing sub-parts for merge. Graphite-style tooling exists for single-checkout workflows, but not for the multi-worktree, multi-repo setup `wgo` users live in.

`ejoffe/spr` solves part of this for linear stacks via commit-id trailers, but it rewrites every commit, assumes a single working tree, and doesn't generalize to a DAG (e.g. a branch that depends on two in-flight refactors).

## Proposed Solution

### 1. Data model

Extend `internal/store/state.go`:

```go
type Annotation struct {
    Purpose   string
    SpecPath  string
    SpecState string
    Parents   []string  // NEW: keys of parent annotations ("repo:branch"); empty == based on default
    StackID   string    // NEW: optional grouping; multiple branches in one named stack
    CreatedAt time.Time
    UpdatedAt time.Time
}

type Stack struct {            // NEW
    ID        string
    Name      string
    RootRef   string            // e.g. "origin/main" — what the roots rebase onto
    CreatedAt time.Time
    UpdatedAt time.Time
}

type State struct {
    // ...existing...
    Stacks map[string]Stack    // NEW: keyed by ID
}
```

The graph is implicit — derived by walking `Annotation.Parents`. No separate edge table.

### 2. New package `internal/stack/`

- `graph.go` — build DAG from `state.Annotations`, topological sort, find roots/leaves, detect cycles, compute "affected descendants" when a node changes.
- `restack.go` — worktree-safe restack algorithm (below).
- `marker.go` — parse and render the `<!-- wgo-stack:<id> -->` block in PR bodies. Renderer emits parents, a PR-number table, and a Mermaid-style ASCII tree so it reads in the GitHub UI.

### 3. Restack algorithm (worktree-safe)

`git rebase --update-refs` is unusable here because `refs/rewritten/*` are worktree-local. Instead, walk the DAG explicitly:

1. `git fetch --all --prune` once at the start.
2. Topologically sort affected nodes from the changed root downward.
3. For each node, in order:
   1. Locate its worktree via `git.ListWorktrees()`. If none exists, create one in `worktrees_dir` (reuse `wgo to`'s creation path).
   2. Refuse to proceed if the worktree is dirty (`git status --porcelain`); print the list and exit non-zero. No autostash — silent stashing during a multi-branch restack is a footgun.
   3. **Single parent**: `git rebase <new-parent-tip>`.
   4. **Multiple parents (merge node)**: rebase onto the first parent's new tip, then `git merge --no-ff <other-parent-tip>` for each remaining parent. This preserves merge topology; rebasing "onto multiple parents" is incoherent.
   5. On conflict: stop, leave the worktree in the rebase/merge state, print the conflicted files and the exact resume command (`wgo stack restack --continue`).
4. After every affected node is rebased cleanly: one `git push --atomic --force-with-lease=<branch>:<expected-old-oid>` per repo, including all touched branches. Capture expected OIDs *before* any local rewrite so the lease actually protects us.
5. For each child whose recorded parent has just merged to default: `gh pr edit <n> --base <new-base>` and remove that parent from `Annotation.Parents`.
6. Re-render the marker block in each affected PR body via `gh pr edit --body`.

`--continue` resumes from a saved checkpoint at `~/.wgo/cache/restack-<stackID>.json` recording the topo order, current index, and the original OIDs for the lease.

### 4. Commands (`wgo stack ...`)

- `wgo stack new <name>` — create a stack rooted at the current branch.
- `wgo stack push <branch> [--on <parent>...] [--draft]` — create branch (worktree) based on parent tip(s), record parents in `Annotation`, push, open draft PR with correct `--base` (first parent), append marker block.
- `wgo stack restack [<branch>] [--continue]` — run the algorithm above starting from `<branch>` (default: nearest ancestor that has been updated since last sync).
- `wgo stack sync` — fetch, detect merged parents, retarget child PR bases, refresh marker blocks. No rebasing.
- `wgo stack status [<id>]` — render the DAG with PR state, CI status, ahead/behind vs parent. Reuse engagement-sort and `gh` integration from `internal/cmd/status.go` and `internal/cmd/pr.go`.
- `wgo stack rm <branch>` — refuse if it has unmerged children; otherwise unregister.

### 5. Integration with existing commands

- `internal/cmd/clean.go` + `internal/cleanup/cleanup.go` — refuse to delete parents with unmerged stack children, even if the parent's own PR is merged.
- `internal/cmd/to.go` — `--on <branch>` flag bases new worktree on a parent and registers `Annotation.Parents`.
- `internal/cmd/dot.go` — when current branch has `StackID`, append a one-line `stack: A → **B** → C` indicator with parent/child PR numbers.
- `internal/plan/plan.go` — render parents inline in active-branches section (`- **repo:branch** ↳ on repo:parent — purpose`). Parser stays tolerant.

## Inputs / Outputs / Contracts

### `internal/git/git.go` additions

```go
// Rebase performs `git rebase <ontoRef>` in the given worktree.
Rebase(worktreePath, ontoRef string) error

// Merge performs `git merge [--no-ff] <ref>` in the given worktree.
Merge(worktreePath, ref string, noFF bool) error

// ForceLeaseRef pairs a branch with the remote OID the caller expects to
// overwrite (used for --force-with-lease=<branch>:<oid>).
type ForceLeaseRef struct {
    Branch      string
    ExpectedOID string
}

// PushForceWithLease performs one atomic `git push --atomic --force-with-lease=...`
// covering all refs at once. Returns an error if any branch lease check fails.
PushForceWithLease(repoPath string, refs []ForceLeaseRef) error

// IsClean reports whether the worktree has no uncommitted changes.
// The second return value lists the dirty paths (porcelain output) for diagnostics.
IsClean(worktreePath string) (bool, []string, error)
```

### `internal/github/prs.go` additions

```go
// UpdatePRBase wraps `gh pr edit <n> --base <b>`.
UpdatePRBase(num int, base string) error

// UpdatePRBody wraps `gh pr edit <n> --body-file -`.
UpdatePRBody(num int, body string) error

// GetPRBody returns the current PR body markdown.
GetPRBody(num int) (string, error)
```

### Marker block format (in PR body)

```
<!-- wgo-stack:<stackID> -->
**Stack** (`<stackID>`):

- #12 base/refactor ← root
- #13 plumbing ↳ on #12
- **#14 feature** ↳ on #13   ← this PR

Generated by `wgo stack`. Edit above/below, not inside this block.
<!-- /wgo-stack -->
```

- The fenced markers delimit a region wgo owns; everything outside is untouched.
- Parsing is tolerant: a missing or malformed block is treated as "no marker present" and rebuilt from local state on the next `wgo stack sync`.

### Checkpoint file (`~/.wgo/cache/restack-<stackID>.json`)

```json
{
  "stackId": "<id>",
  "topoOrder": ["repo:a", "repo:b", "repo:c"],
  "currentIndex": 1,
  "leases": {"repo:a": "<oid>", "repo:b": "<oid>", "repo:c": "<oid>"},
  "startedAt": "2026-05-22T..."
}
```

Deleted on successful completion. Loaded by `wgo stack restack --continue`.

## Edge Cases & Constraints

- **Cycle detection**: `internal/stack/graph.go` rejects any topology where a branch reaches itself via `Parents`. New `wgo stack push --on X` calls that would close a cycle exit non-zero before writing state.
- **Dirty worktree mid-restack**: refuse and exit; print path and porcelain output. User commits/stashes manually, then re-runs.
- **Conflicts during rebase or merge**: leave the worktree in the conflict state, write the checkpoint, and instruct the user to resolve and run `wgo stack restack --continue`.
- **Lease failure on push**: `git push --force-with-lease` rejects when remote OID has diverged. Print the failing branch and instruct user to fetch and rerun.
- **Missing worktree for a stack node**: lazy-create in `worktrees_dir` using the same path template `wgo to` uses.
- **Marker block edited by hand on GitHub**: parser tolerates malformed/edited blocks; `wgo stack sync` overwrites with the canonical version.
- **PR body length**: GitHub caps PR bodies at 65,536 chars. Marker block is well under 1KB even for large stacks; no truncation concern.
- **Parent recorded but parent branch missing locally**: treat as not-discovered; warn and skip in restack walk rather than failing the whole operation.
- **Cross-repo stacks**: out of scope for this spec (parents are scoped to the same repo). A stack with members across repos can be tracked via `Effort` but not restacked atomically.
- **No PR yet for a stack node**: marker block updates and base retargeting silently skip nodes with `PRNumber == 0`. Local rebase still runs.
- **`gh` not installed**: stack commands that need GitHub mutations (`stack push --draft`, `stack sync`, marker refresh in `stack restack`) fail with a clear error pointing at the `gh` install. Pure-local commands (`stack new`, `stack status` against cached data) still work.
- **Default branch detection**: `Stack.RootRef` defaults to `origin/<default>` via the same lookup `wgo to` uses (`git.DefaultBranch`), refreshed lazily.

## Out of Scope

- Cross-repo stacks (parents in a different repo). Track via `Effort` for now.
- Importing existing pre-stacked PRs that weren't created by `wgo stack push`. A follow-up `wgo stack adopt <pr-number>` can heuristically infer parents from PR bases.
- Stack-aware merge-queue interaction (merging an intermediate PR atomically with its descendants). GitHub MQ doesn't support this primitive yet.
- Squash-merge fixup (rewriting child branches' parents to the squash commit on default). The `sync` retarget-to-default path covers this; preserving authorship of squashed-away commits is not attempted.
- A TUI for stack visualization. `wgo stack status` is text-only for this spec.
- Optional `jj` accelerator path (use `jj rebase -s ... -d ...` when a colocated jj repo is present). Considered and deferred — git-only for this ticket.
- Automatic stack creation from `git log --graph` heuristics. All stack membership is explicit via `wgo stack new/push`.

## Acceptance Criteria

- [ ] `wgo stack new <name>` registers the current branch as a stack root in `state.json`; idempotent on re-run.
- [ ] `wgo stack push <child> --on <parent>` creates a worktree on the parent's tip, records `Annotation.Parents = [parent]`, pushes the new branch, and opens a draft PR with `--base <parent>`.
- [ ] `wgo stack push <child> --on <parent1> --on <parent2>` records both parents (multi-parent / merge node).
- [ ] `wgo stack restack <branch>` rebases every affected downstream branch in topological order, each in its own worktree, then performs one atomic `git push --atomic --force-with-lease=...` per repo.
- [ ] On rebase conflict, `wgo stack restack` leaves the conflicted worktree in place, writes a checkpoint, and prints the resume command. `wgo stack restack --continue` resumes from the next un-rebased node without redoing earlier ones.
- [ ] Multi-parent restack rebases onto the first parent then `git merge --no-ff`s each remaining parent into the result.
- [ ] `wgo stack sync` retargets child PR bases to `origin/<default>` when a recorded parent has merged, removes that parent from `Annotation.Parents`, and rebases the child onto the new base.
- [ ] PR bodies show a `<!-- wgo-stack:<id> -->` block that lists all stack members with their PR numbers and parent relationships. Editing text outside the block is preserved on `wgo stack sync`.
- [ ] `wgo clean` refuses to delete a parent branch (local branch, worktree, or remote) while any descendant has an unmerged PR, even if the parent's own PR is merged.
- [ ] `wgo to --on <branch>` bases the new worktree on the given branch's tip and registers `Annotation.Parents = [branch]`.
- [ ] `wgo .` displays a `stack: A → **B** → C` line with PR numbers when the current branch belongs to a stack.
- [ ] `wgo plan` shows parent relationships inline in the active-branches section without breaking the existing parser.
- [ ] Cycle prevention: `wgo stack push --on X` that would create a cycle exits non-zero with a clear error and writes no state.
- [ ] Unit tests cover topological sort (linear, fan-out, merge-node), cycle detection, affected-descendants, marker parse/render roundtrip including malformed blocks.
- [ ] End-to-end test against a throwaway repo exercises: linear 3-branch stack restack, merge-node restack, parent-merge sync with base retarget, clean safety with unmerged children, conflict-resume happy path.
