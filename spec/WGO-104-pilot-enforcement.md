---
ticket: WGO-104
title: Pre-commit spec enforcement and pilot summary generator
status: draft
authors: [dmihalcik, sujan]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
phase: 4
estimate: 2d
depends_on: [WGO-101, WGO-102, WGO-103]
---

# WGO-104 — Pre-commit spec enforcement and pilot summary generator

## Summary

Two end-of-pilot capabilities:

1. an **opt-in pre-commit hook** that blocks commits on branches without a referenced spec, with sensible escape hatches;
2. **`wgo pilot-summary`**, which walks daily logs and GitHub data over a date range and produces a markdown draft mapped to the pilot's required workflow-summary sections.

## Problem / Motivation

Without enforcement, the pilot relies on willpower — easy to forget the spec when shipping a fix at 5pm. A configurable block flips spec-first from "best effort" to "the path of least resistance is to spec first."

For the workflow summary, the pilot's deliverable maps to data wgo already has (commits, PRs, reviews, daily logs); generating a draft saves the pair from manually reconstructing the month and ensures the metrics are accurate. Honest, automatic metrics also let the pilot organizers compare across pairs without bias.

## Proposed Solution

1. **`HandlePreCommit`** in `internal/hooks/event.go`. Triggered by a new pre-commit script installed by `wgo hooks install`.
2. **Config flag**: `[hooks] spec_required = false` (default) gates enforcement.
3. **Allowance rules** (commit succeeds if **any** is true):
   - Branch is in `[hooks] exclude_branches` (e.g., `main`, `master`)
   - Staged diff only touches `spec/` (you're committing a spec; can't require one to do so)
   - Commit message contains `[no-spec]` (escape hatch for emergencies)
   - `Annotation.SpecPath` exists for the branch AND the file exists on disk
   - Commit message body contains `Spec: spec/...` line
   - Commit's added/modified line count ≤ `[hooks] spec_required_min_lines`
4. **`wgo pilot-summary`**: walks `~/.wgo/logs/`, calls `gh` for PR/review data, reuses `internal/spec/locate.go` for spec inventory, emits a markdown draft.

## Inputs / Outputs / Contracts

### Config (`internal/config/config.go`)

```toml
[hooks]
enabled                  = true
auto_plan                = false
exclude_branches         = ["main", "master"]
spec_required            = false   # NEW
spec_required_min_lines  = 5       # NEW; commits ≤ N changed lines bypass the check
```

### `HandlePreCommit` (`internal/hooks/event.go`)

```go
type PreCommitContext struct {
    RepoRoot    string
    Branch      string
    StagedFiles []string
    StagedDiff  string  // optional; loaded only if needed
    CommitMsg   string  // from .git/COMMIT_EDITMSG
}

type PreCommitDecision struct {
    Allow  bool
    Reason string  // shown to the user (success or failure)
}

func HandlePreCommit(ctx PreCommitContext, cfg *config.Config) (PreCommitDecision, error)
```

Hook script installed at `~/.wgo/hooks/pre-commit`:

```sh
#!/bin/sh
exec wgo _event pre-commit \
  --branch "$(git rev-parse --abbrev-ref HEAD)" \
  --staged "$(git diff --cached --name-only | tr '\n' ',')" \
  --msg-file "$(git rev-parse --git-dir)/COMMIT_EDITMSG"
```

Failure output (stderr):

```
✗ commit blocked: branch WGO-201-fix-bug has no spec.

Options:
  • Create one:  wgo spec new WGO-201
  • Reference one in this commit message:
        Spec: spec/WGO-201.md
  • Skip for this commit (e.g., emergency):
        git commit -m "your message [no-spec]"
  • Disable globally:  set [hooks] spec_required = false in ~/.wgo/config.toml
```

Exit code: `1` to abort the commit.

### `wgo pilot-summary` (`internal/cmd/pilot.go`, `internal/pilot/summary.go`)

```
wgo pilot-summary --since 2026-05-01 --until 2026-05-31 \
                  [--team dmihalcik,sujan-tcube] \
                  [--output FILE] [--json]
```

Aggregates:

| Metric                       | Source                                                                                  |
| ---------------------------- | --------------------------------------------------------------------------------------- |
| Specs created                | `git log --diff-filter=A -- spec/*.md` across discovered repos                          |
| Specs updated                | `git log -- spec/*.md`; count per spec                                                  |
| PRs merged                   | `gh pr list --search "merged:>=YYYY-MM-DD merged:<=... author:..."`                     |
| Spec → PR cycle time         | First commit of `spec/<TICKET>.md` → first commit of code on branch with same TICKET   |
| Review iterations            | `gh pr view --json reviews` per merged PR; count distinct review submissions            |
| Drift events caught          | Daily logs from `~/.wgo/logs/`, parsed for "drift detected" markers logged by WGO-102   |
| Spec edits per pair member   | Commits to `spec/*.md` grouped by author                                                |
| Pre-commit blocks            | Optional — if WGO-104 instrumented, count from daily logs                               |
| `[no-spec]` overrides        | `git log --grep="\[no-spec\]"` over the period                                          |
| Pilot checklist              | Static — emit the 5-line checklist with `[x]` for items wgo can verify                  |

### Markdown output template

```markdown
# May 2026 Pilot Workflow Summary — Dave + Sujan

_Period: 2026-05-01 to 2026-05-31_

## How we structured pairing
- Spec authorship distribution: Dave 12 specs, Sujan 8 specs, joint 4 specs
- Average pair window: <derived from overlapping commit timestamps>
- > _your notes here: live pairing? async? rotation pattern?_

## Spec → implementation handoff
- Median time from spec creation to first impl commit: 2h 14m
- 18 specs followed the spec-first rule; 2 had impl commits before spec
- > _your notes here: how the handoff felt; tooling friction_

## What worked
- > _your notes here_

## What we'd do differently
- > _your notes here_

## Metrics
| Metric | Value |
|---|---|
| Specs created | 24 |
| Specs updated post-creation | 31 (avg 1.3 updates per spec) |
| PRs merged | 22 |
| Median spec → PR cycle time | 2d 4h |
| Median review iterations per PR | 2 |
| Drift events caught | 5 |
| Pre-commit blocks | 12 (with spec_required=true) |
| `[no-spec]` overrides | 1 |

## Pilot checklist
- [x] Aligned on working style/schedule (recorded in spec/WGO-101.md frontmatter)
- [x] Written and committed a spec before starting each work item (verified above)
- [x] Specs stored in /spec/ at repo root
- [x] Specs updated when scope/approach changed (31 update events)
- [ ] Workflow summary submitted (this document)
```

## Edge Cases & Constraints

- **Hook performance budget**: pre-commit must run in <50ms p95. Avoid loading `state.json` if branch is in `exclude_branches`. Read frontmatter only when needed.
- **Hook bypass via `git commit --no-verify`**: cannot prevent at the wgo layer. Document this as expected; pilot relies on culture, not coercion.
- **`[no-spec]` in commit message body but not subject**: regex match on entire message, not just subject.
- **Trivial commits**: `[hooks] spec_required_min_lines = 5` lets typo-fixes through without ceremony.
- **First commit on a branch where the branch was created via `wgo add`**: the spec scaffold commit itself touches `spec/` only — passes the "diff only touches spec/" rule.
- **Detached HEAD / rebase scenarios**: skip enforcement when `git rev-parse --abbrev-ref HEAD` returns `HEAD`.
- **`pilot-summary` time-zone**: dates are interpreted in the user's local timezone (`time.Local`); the summary header notes this.
- **`pilot-summary` for repos that don't yet have `spec/` folder**: gracefully count zero, don't error.
- **`gh` rate limits**: `pilot-summary` may need 100+ PR fetches. Use the existing TTL cache; surface a warning if rate-limit headers indicate <10% remaining.
- **Privacy**: `pilot-summary` only queries repos in `state.repos` (which the user has already opted into via `wgo track`).
- **Multiple authors on one spec**: a spec with `authors: [dave, sujan]` counts in both engineers' "Specs created" tallies; the joint column is also incremented. Total specs ≠ sum of per-author specs.

## Out of Scope

- Server-side enforcement (a GitHub branch protection rule that requires a `spec/*.md` file in the PR diff). Out of scope for the CLI; the pair can configure GitHub manually if they want server-side teeth.
- Per-organization rollout helpers (Slack bot, etc.).
- Auto-publishing the pilot summary to Confluence — output is a markdown file the user copy-pastes.
- Scoring or ranking pairs against each other.
- Custom hooks beyond pre-commit (e.g., `commit-msg`, `prepare-commit-msg`). The single pre-commit point is sufficient.
- Retroactive enforcement for branches already created without a spec — `wgo spec link` (WGO-102) is the path.

## Acceptance Criteria

- [ ] `[hooks] spec_required = true` causes `git commit` to fail on a branch with no spec, no `[no-spec]` marker, and no `Spec:` reference, with a clear remediation message
- [ ] All allowance rules (excluded branch, spec-only diff, `[no-spec]`, spec exists, `Spec:` reference, min-lines) work as documented
- [ ] `[hooks] spec_required = false` (default) preserves current behavior — hook is a no-op
- [ ] `wgo hooks install` writes the pre-commit script and reports success
- [ ] `wgo hooks status` reports whether `spec_required` is on or off
- [ ] Pre-commit runs in <50ms p95 on a 100-file staged diff (measured by integration test)
- [ ] `git commit --no-verify` bypasses as expected (documented, not test-enforced)
- [ ] `wgo pilot-summary --since 2026-05-01 --until 2026-05-31` produces a markdown document with all 5 required sections plus the pilot checklist
- [ ] `wgo pilot-summary --json` emits structured data
- [ ] All metrics in the table populate from real data (no `0` placeholders) when run against a repo with spec activity
- [ ] `pilot-summary` finishes in <30s for a 1-month window with 50 PRs
- [ ] Tests in `internal/hooks/event_test.go` cover all allowance rules; `internal/pilot/summary_test.go` covers metric aggregation against a fixture git repo
