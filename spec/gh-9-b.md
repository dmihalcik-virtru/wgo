---
ticket: gh-9-b
title: Stack-first sync and checkout with GitHub's native gh-stack
status: draft
authors: [dmihalcik]
branches: []
prs: []
issue: https://github.com/dmihalcik-virtru/wgo/issues/9
created: 2026-07-31
updated: 2026-07-31
phase: 1
estimate: 3d
depends_on: [gh-21]
supersedes_ux: gh-9
---

# gh-9-b — Stack-first sync & checkout, integrated with GitHub's native `gh stack`

## Summary

Make the **stack** wgo's unit of work — where a lone PR is simply a one-entry
stack handled by the same code paths — across both directions:

- **Publish:** `wgo sync` publishes its jj-derived topology to GitHub's native
  **Stack** object via `gh stack link`, and `wgo` gains PR creation.
- **Consume:** `wgo to` checks out the *entire* stack containing a target PR
  (reconstructed from PR base refs, no `gh stack` dependency) so you can build
  atop the leaf or fork a DAG branch off an interior node.

jj remains the single source of truth for topology; `gh stack` is an optional,
stateless *publishing* target used only through its no-local-tracking command
(`gh stack link`). When a native GitHub Stack is present, `wgo` stops emitting
its hand-rolled `wgo-stack` marker block.

## Problem / Motivation

Two gaps and one redundancy:

1. **`wgo` cannot create PRs.** `internal/github` only PATCHes existing PRs
   (`UpdatePRBase`, `UpdatePRBody`, `ClosePR`) — see `internal/github/github.go`.
   A user or the GitHub UI must open every PR before `wgo sync` can retarget it.
2. **`wgo`'s stack linkage is a hand-rolled hack.** `internal/sync/marker.go`
   embeds a `<!-- wgo-stack:<id> -->` block (plus a JSON sidecar) in each PR body
   so reviewers see topology. GitHub now ships a **native Stack** object that does
   this properly.
3. **`gh stack` exists and duplicates our topology brain.** GitHub's
   `gh stack` extension (`gh extension install github/gh-stack`, requires
   gh ≥ 2.90) creates stacked PRs, sets bases, and links them into a native
   Stack. But it tracks topology in `.git/gh-stack` (a local uncommitted JSON
   file) and drives `git rebase`/`git push` — a second source of truth that
   would drift from, and fight, the jj DAG.

We want `gh stack`'s two genuine wins — PR creation and native Stack linking —
without adopting its shadow-state or git-rebase machinery, which gh-21
deliberately removed.

## Proposed Solution

### Core stance

- **Stack-first.** The stack is wgo's unit of work; a lone PR is a one-entry
  stack handled by the same code paths. `wgo to`, `wgo sync`, and PR creation
  all operate on stacks and degenerate cleanly to size 1 — no separate
  "single PR" path.
- **jj DAG stays authoritative** for topology (`internal/sync/graph.go`
  `BuildFromLog` / `TopoSort` / `NearestAncestorWith` are unchanged).
- **`gh stack` is a stateless publishing target.** `wgo` uses **only**
  `gh stack link <branch>...` — documented as "creates or updates a stack on
  GitHub **without local tracking**." This never creates or reads
  `.git/gh-stack` and never runs a git rebase.
- **Never invoke** `gh stack {init, add, rebase, sync, modify, submit}` — those
  are the commands that write `.git/gh-stack` and run git operations that would
  conflict with jj auto-restacking.
- **Optional, degrades gracefully** — same posture as the existing
  `gh auth token` shell-out. No `gh` / non-colocated repo / gh < 2.90 → fall
  back to today's marker behavior.

### 1. Add PR creation to `internal/github`

Add a native REST `CreatePR` (the `postJSON` helper already exists but is
unused for this):

```go
// CreatePR opens a pull request via POST /repos/{slug}/pulls and returns the
// created PR. head/base are branch names; draft controls the draft flag.
CreatePR(repoPath string, opts CreatePROpts) (PRInfo, error)

type CreatePROpts struct {
    Head  string // topic bookmark (exported git branch)
    Base  string // parent bookmark or default branch
    Title string
    Body  string
    Draft bool
}
```

This keeps the no-`gh`-required degradation path intact: PR creation works over
plain HTTPS even when `gh stack` is unavailable.

### 2. New GitHub-Stack publisher

New file `internal/sync/ghstack.go` wrapping the CLI:

```go
// Linker publishes an ordered stack of branches to GitHub's native Stack.
type Linker interface {
    Available() bool                                  // gh + gh-stack + colocated repo present
    Link(repoPath string, orderedBranches []string) error // shells `gh stack link ...`
}
```

`orderedBranches` is bottom→top, derived from the existing `Graph.TopoSort()`.
`Available()` gates on: `gh` on PATH, `gh extension list` includes
`github/gh-stack`, gh version ≥ 2.90, and the repo is colocated
(`jj.IsColocated`) so a `.git` dir exists for `gh` to read.

### 3. Wire it into `wgo sync`

`internal/sync/sync.go` already isolates the write surface behind `GitHubOps`
(`GetPRStatus`, `UpdatePRBase`, `GetPRBody`, `UpdatePRBody`). Extend the
algorithm:

1. `jj git fetch` (unchanged).
2. Build graph from jj log (unchanged).
3. **New:** for each bookmarked node with no open PR, if `--create-prs` (or
   config `sync.create_prs`), call `CreatePR` with base = nearest ancestor with a
   PR (else default base). Draft by default.
4. Retarget bases via `UpdatePRBase` (unchanged) — still the authority on base
   correctness even when a native Stack exists.
5. **New (branch on linker availability):**
   - If `Linker.Available()` **and** stack has ≥ 2 PRs: call
     `Link(repo, TopoSort())` to create/update the native Stack, and **skip**
     marker generation. If any node's PR body already contains a `wgo-stack`
     marker, strip it via `UpdatePRBody` (clean migration off the marker).
   - Else: regenerate the marker as today (`refreshMarker` + `UpdatePRBody`).

### 4. Consumer side: `wgo to` checks out the whole stack

**The stack is the unit; a lone PR is a one-entry stack.** `wgo to` always
resolves the full stack containing the target and fetches it. The common
single-PR case falls out of the same code path with a stack of length 1 — there
is no separate single-PR branch in the code.

**Stack resolution — reconstruct from PR base refs, not from `gh stack`.** Given
the target (PR URL / number / branch), walk **down** via `base` refs to trunk
and **up** by finding open PRs whose `base` equals each head, using the existing
`ListPRsForBranch` / `SearchPRs`. Result: an ordered bottom→top list. A PR with
no stacked neighbors yields a one-entry stack. Enumeration lives in a shared
helper so `sync` (publish) and `to` (consume) agree on ordering, and it depends
on neither the native Stack object nor the `wgo-stack` marker — so it works even
when the author used plain git/gh.

**Fetch + checkout.** `jj git fetch` all head branches in the stack (one fetch,
multiple `--branch` globs), `jj.BookmarkTrack` each so the local DAG carries full
ancestry. Open a workspace landed on the **named node** (the PR you passed).

**Forking is the same op for leaf or interior** — jj's DAG doesn't distinguish:

```
wgo to <PR-URL>                 # fetch whole stack, land on that node
jj new <node>                   # build atop the leaf, OR fork an interior node
# ...work...
jj bookmark create my-work -r @
wgo sync --create-prs           # opens YOUR PR, based on <node>
```

`wgo to <PR-URL> --on <bookmark>` lands the workspace on an interior node for
forking off the middle of someone else's stack (repurposes the existing `--on`
flag in `to.go`, currently a no-op stub). Your new PR's base is the forked-from
node — chosen by the existing `NearestAncestorWith`, no interior/leaf special
case.

**Staying current when the owner restacks:**

```
jj git fetch                    # brings the rewritten stack
jj rebase -d <node>@origin      # replant your fork onto the moved node
```

jj records conflicts as conflict commits rather than halting, so you keep
working and resolve when convenient.

### 5. Config

`config.toml` additions (read via `internal/config`):

```toml
[sync]
create_prs   = false   # open draft PRs for bookmarks that lack one
gh_stack     = "auto"   # "auto" | "on" | "off" — use native gh stack linking
```

`auto` = use native linking whenever `Linker.Available()`; `off` forces the
marker path; `on` errors if `gh stack` is unavailable rather than silently
falling back.

### 6. Agent / skills guidance

`gh stack` also installs as an agent **skill** (`gh skill install
github/gh-stack`). Because wgo is AI-agent-first, document the composed,
safe workflow in `CLAUDE.md` and/or a wgo skill so agents don't corrupt jj's
view:

- jj creates changes; `wgo sync` derives topology and links via `gh stack link`.
- Agents must **never** run `gh stack {init,add,rebase,sync,modify,submit}` in a
  wgo/jj workspace — those write `.git/gh-stack` and run git rebases.

## Inputs / Outputs / Contracts

- `CreatePR` → `POST /repos/{owner}/{repo}/pulls`, returns `PRInfo`
  (`internal/github/github.go:241`). Slug resolved via existing `resolveSlug`.
- `Linker.Link` → `gh stack link <b0> <b1> ... <bn>` run in the colocated repo
  root, bottom→top. Non-zero exit maps to a typed error surfaced by `wgo sync`.
- Bookmark export: before any create/link, ensure each bookmark points at its
  change and is pushed (`jj.BookmarkSet(name, "@-")` per gh-21 Risk 1, then
  `jj.GitPush`) so `gh` sees the branches as real git refs.
- Stack enumeration (consumer side), shared by `to` and `sync`:

```go
// ResolveStack reconstructs a stack (bottom→top) from any member PR by walking
// base refs via the GitHub API. A lone PR returns a one-element slice.
ResolveStack(repoPath string, member PRRef) ([]StackMember, error)

type StackMember struct {
    Branch   string
    PRNumber int
    Base     string
}
```

## Edge Cases & Constraints

- **Non-colocated workspace** (jj-only, no `.git`): `Linker.Available()` false;
  fall back to marker. This covers `jj workspace add` workspaces (gh-21
  constraint 2: only main checkouts colocate).
- **`gh stack` not installed / gh < 2.90:** `auto` falls back to marker; `on`
  errors with an install hint.
- **Single-PR stack:** no native Stack and no marker needed; skip linking.
- **Existing marker + now using native Stack:** strip the marker region from the
  PR body on the first native-link sync (parser in `marker.go` already tolerates
  and locates the region).
- **`gh stack link` and `UpdatePRBase` disagree:** `UpdatePRBase` runs first and
  is authoritative for base refs; `link` only establishes the native grouping.
- **Cross-repo stacks:** out of scope (gh-21 constraint 7). Linking is per-repo.
- **PR body owned by user outside marker:** with the marker dropped, `wgo` no
  longer writes to PR bodies at all in the native path — user text is untouched.
- **Target not in any stack (consumer):** one-entry stack; `wgo to` behaves
  exactly as today for a single PR.
- **Interior node requested but ancestors not all fetchable** (a base branch
  already merged/deleted): fetch what exists, warn, and land on the node anyway;
  jj shows the missing base as a loose root.
- **Fork off a node whose PR later merges:** existing `wgo sync`
  retarget-to-default logic moves your base — no new behavior.
- **Owner used plain git/gh (no native Stack, no marker):** the base-ref walk
  still reconstructs the stack; the consumer side needs neither `gh stack` nor
  markers.

## Out of Scope

- Using `gh stack {init,add,rebase,sync,modify,submit}` — permanently excluded;
  they introduce `.git/gh-stack` shadow state and git rebases.
- Migrating `.git/gh-stack` state into jj (never created by wgo).
- Cross-repo native stacks.
- Reading GitHub's **native Stack object** to reconstruct topology. The consumer
  side (§4) reconstructs from PR base refs via the API instead, so it works
  regardless of whether the author used `gh stack`. jj stays the source of truth
  for *your* local topology; the base-ref walk only bootstraps a checkout.
- A `wgo stack` command resurrection — `wgo sync` remains the single entry point.

## Acceptance Criteria

- [ ] `internal/github` exposes `CreatePR` over HTTPS (no `gh` shell-out);
      opens a draft PR with correct head/base.
- [ ] `wgo sync --create-prs` opens draft PRs for bookmarked changes that lack
      one, basing each on the nearest ancestor with a PR (else default base).
- [ ] `internal/sync` has a `Linker` with `Available()` gating on gh + gh-stack
      + gh ≥ 2.90 + colocated repo.
- [ ] With `gh_stack = "auto"` and a ≥ 2-PR stack in a colocated repo,
      `wgo sync` calls `gh stack link` with bottom→top branch order and creates
      a native GitHub Stack.
- [ ] When native linking is used, `wgo sync` does **not** write a `wgo-stack`
      marker, and strips any pre-existing marker block from affected PR bodies.
- [ ] With `gh_stack = "off"` (or `gh stack` unavailable under `"auto"`),
      behavior is identical to today's marker path.
- [ ] `gh_stack = "on"` with `gh stack` unavailable exits non-zero with an
      install hint.
- [ ] `wgo sync` never invokes `gh stack init/add/rebase/sync/modify/submit`
      and never creates `.git/gh-stack` (asserted in tests).
- [ ] `CLAUDE.md` (or a skill) documents the safe agent workflow and the banned
      `gh stack` subcommands.
- [ ] Unit tests: `CreatePR` request shaping; `Linker.Available()` gating
      matrix; sync branch selection (marker vs native) driven by config +
      availability; marker-strip on migration.
- [ ] End-to-end test against a throwaway colocated repo: build a 3-bookmark jj
      stack, `wgo sync --create-prs`, assert 3 PRs with correct bases and a
      native Stack, and no marker blocks.

### Consumer side (`wgo to`)

- [ ] `wgo to <PR-URL>` fetches every head branch in the target's stack and
      tracks each as a bookmark; a lone PR fetches exactly one branch via the
      same code path (one-entry stack).
- [ ] Stack enumeration reconstructs bottom→top order from PR base refs via the
      GitHub API, with no `gh stack` invocation and no dependence on markers or
      the native Stack object. Shared helper is used by both `to` and `sync`.
- [ ] `wgo to <PR-URL> --on <bookmark>` opens the workspace on the named interior
      node so `jj new` forks a DAG branch there.
- [ ] After forking and `wgo sync --create-prs`, the new PR's base is the
      forked-from node (leaf or interior), chosen by `NearestAncestorWith`.
- [ ] E2E: check out a 3-PR stack authored elsewhere, fork off the interior
      node, open a PR based on it; simulate an upstream restack and confirm
      `jj git fetch` + `jj rebase -d <node>@origin` replants the fork.
