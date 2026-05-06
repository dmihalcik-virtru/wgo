---
ticket: WGO-102
title: wgo spec command family and drift detection
status: draft
authors: [dmihalcik, sujan]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
phase: 2
estimate: 2d
depends_on: [WGO-101]
---

# WGO-102 — `wgo spec` command family and drift detection

## Summary

Ship the user-facing `wgo spec` subcommand tree (`new`, `show`, `edit`, `ls`, `status`, `link`), plus a drift detector that flags branches whose implementation has outpaced their spec. Add a Spec column to `wgo status`.

## Problem / Motivation

WGO-101 makes specs exist; WGO-102 makes them usable.

Without a list view a pair can't see what specs are in flight or filter by status. Without `edit` and frontmatter auto-updates, the `updated:` field rots and the spec drifts silently from the implementation. Without a Spec column in `wgo status`, the ambient signal during the day doesn't include "is my spec stale?". And without `link`, branches that pre-date `wgo add` (or were created with `--no-spec`) can't be retroactively connected to a spec.

## Proposed Solution

1. **New cobra subcommand: `wgo spec`** in `internal/cmd/spec.go`, mirroring the structure of `internal/cmd/plan.go`.
2. **Drift detector** in `internal/spec/drift.go` — pure functions over git + spec frontmatter, no command/IO coupling so it's reusable from `wgo status`, `wgo .`, and (later) `wgo pilot-summary`.
3. **`wgo status` column** — add an optional Spec column to the existing renderer in `internal/cmd/status.go`. Reads `Annotation.SpecState` first; falls back to a single-shot `spec.Parse` call.
4. **Auto-bump frontmatter** on `wgo spec edit`: after `$EDITOR` exits with a modified file, re-read frontmatter and bump `updated:` to today. Update `Annotation.SpecState` if `status:` changed.

## Inputs / Outputs / Contracts

### Cobra subcommand tree (`internal/cmd/spec.go`)

```
wgo spec
├── new <TICKET> ["title"]                  # creates spec/<TICKET>.md if missing; links to current branch
├── show [TICKET]                           # current branch's spec, or named; rendered to stdout
├── edit [TICKET]                           # opens in $EDITOR; bumps `updated:` on save
├── ls [--all-repos] [--status STATE]       # table: ticket | status | authors | branches | updated
│       [--mine|--pair|--team] [--json]
├── status [--all-repos]                    # drift report; nonzero exit if drift detected
└── link <TICKET>                           # associates current branch with existing spec; updates frontmatter.branches
```

### Flag semantics

| Flag           | Default                | Effect                                                                                      |
| -------------- | ---------------------- | ------------------------------------------------------------------------------------------- |
| `--all-repos`  | `false`                | Walk all repos in `state.repos` instead of just cwd                                         |
| `--status STATE` | `""` (any)           | Filter `ls` to specs in the given lifecycle state (draft / in_progress / shipped / abandoned) |
| `--mine`       | implicit if no pair flag | Filter to specs where `cfg.Author` ∈ `authors`                                             |
| `--pair`       | `false`                | Filter to specs where both `cfg.Author` and `cfg.Pair.Teammate` ∈ `authors`                 |
| `--team`       | `false`                | No author filter — show all                                                                 |
| `--json`       | `false`                | Machine-readable output for `ls` and `status`                                               |

### `internal/spec/drift.go`

```go
type DriftKind string
const (
    DriftStale     DriftKind = "stale"      // commits authored after spec.Updated
    DriftUntracked DriftKind = "untracked"  // branch parses to TICKET but no spec/<TICKET>.md
    DriftOrphaned  DriftKind = "orphaned"   // spec exists, status not terminal, no live branch references it
    DriftSpecOnly  DriftKind = "spec_only"  // spec edited recently but no impl commits — informational
)

type DriftReport struct {
    Kind     DriftKind
    Branch   string  // "" for orphaned
    Spec     string  // "" for untracked
    Detail   string  // human-readable extra
    Severity int     // 0=info, 1=warn, 2=error
}

func DetectForBranch(repoRoot, branch string) ([]DriftReport, error)
func DetectAll(repoRoot string) ([]DriftReport, error)
```

`DriftStale` heuristic: count commits on `branch` since `spec.Frontmatter.Updated` where the commit touches files outside `spec/`. If count > 0, emit stale with severity 1 and `Detail` carrying the count.

### `wgo status` Spec column

| Glyph | Meaning                                                                  |
| ----- | ------------------------------------------------------------------------ |
| `●`   | draft                                                                    |
| `◐`   | in_progress                                                              |
| `✓`   | shipped                                                                  |
| `−`   | abandoned                                                                |
| `⚠`   | missing (branch should have one but doesn't)                             |
| `↯`   | drift (stale spec)                                                       |
| ` `   | branch isn't expected to have a spec (e.g. `main`, `exclude_branches`)   |

Toggle off via `--no-spec-column` or `[status] show_spec_column = false` in `config.toml`.

### `Annotation.SpecState` cache invalidation

`Annotation.SpecState` is updated in three places:

1. `wgo add` writes initial `"draft"` (WGO-101).
2. `wgo spec edit` re-reads frontmatter on save, writes the new status.
3. Lazy refresh in any reader: if spec file `mtime > annotation.UpdatedAt`, re-parse and write through.

## Edge Cases & Constraints

- **`wgo spec new WGO-101` when `spec/WGO-101.md` already exists**: error with hint to use `wgo spec edit WGO-101`.
- **`wgo spec link` on a branch whose name doesn't parse to a ticket**: take a `--ticket WGO-101` override; otherwise error.
- **`wgo spec edit` on a spec with malformed frontmatter**: warn, open the file as-is, do not auto-bump `updated:` (avoid clobbering broken frontmatter further).
- **`wgo spec ls --all-repos`** discovery scope: reuse `internal/discovery/` walker. Skip repos where `state.json` last-seen > `[discovery] stale_days`.
- **Drift on branches without a remote**: still detect, but `DriftSpecOnly` requires `git log` — handle empty branches gracefully (no commits since branch creation).
- **Ticket case**: branch parser is case-insensitive (`wgo-101` parses to `WGO-101`); locator normalizes to upper-case for filename match.
- **Multi-repo specs**: `wgo spec ls` deduplicates by ticket — one spec, possibly in repo A, can be referenced by branches in repo B. Render shows all referenced branches.
- **Performance**: `wgo status` with N branches makes N spec reads. Use the `SpecState` cache; cap concurrent reads at GOMAXPROCS.

## Out of Scope

- Pair-specific filters beyond `--pair` flag plumbing (full lighting in WGO-103).
- Pre-commit hook integration (WGO-104).
- A web/desktop UI for spec management — terminal only.
- Editing the spec body programmatically — `wgo spec edit` opens `$EDITOR`; no field-level CLI editing.
- Auto-detecting "shipped" status from PR merge events (could be a follow-on; for now `status:` is user-managed).

## Acceptance Criteria

- [ ] `wgo spec new WGO-200 "title"` creates `spec/WGO-200.md` from template, links to current branch in plan and state, exits 0
- [ ] `wgo spec show` (no arg) on a branch with a spec prints the file
- [ ] `wgo spec edit` opens `$EDITOR`; on close, `Updated` field equals today's date even if user only changed body
- [ ] `wgo spec ls` outputs a sorted table with one row per spec
- [ ] `wgo spec ls --json` emits parseable JSON (validated by `jq`)
- [ ] `wgo spec ls --mine` and `--pair` filter as documented
- [ ] `wgo spec status` flags a `stale` report when there's an implementation commit after the spec's `updated:` field; exits with code 1
- [ ] `wgo spec status` flags `untracked` when on a branch named `WGO-201-foo` with no `spec/WGO-201.md`
- [ ] `wgo status` table shows the Spec column with the correct glyph; `--no-spec-column` removes it
- [ ] `Annotation.SpecState` refreshes when the spec file is touched out-of-band
- [ ] All new behavior covered by tests in `internal/cmd/spec_test.go` and `internal/spec/drift_test.go`
