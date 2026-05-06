---
ticket: WGO-103
title: Pair awareness across status, today, pr, and plan
status: draft
authors: [dmihalcik, sujan]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
phase: 3
estimate: 2d
depends_on: [WGO-101, WGO-102]
---

# WGO-103 — Pair awareness across status, today, pr, and plan

## Summary

Teach `wgo` about a teammate. Add a `[pair]` block in config; thread it through `wgo today`, `wgo pr`, `wgo status`, and `wgo plan`; ship a new `wgo team` two-column dashboard. The shared surface is GitHub (the spec is in the repo; PRs are visible to the team) — no `state.json` sync.

## Problem / Motivation

Pair programming during the pilot can be remote, async, or live. In all three modes, both engineers benefit from seeing each other's branches, PRs, reviews, and spec edits in one place. Today wgo's lens is single-user: `cfg.Author` is a single string used for filtering. Switching to a pair-aware lens — without redesigning state — lets either engineer drop into the other's context with one command and lets `wgo today` produce a "we" report instead of a "me" report.

A second motivation: the pilot's workflow summary (WGO-104) needs accurate per-author and joint metrics. The data plumbing introduced here feeds that report.

## Proposed Solution

1. **Config**: add a `[pair]` block to `internal/config/config.go` with `teammate`, `teammate_jira`, `display_name`, optional `teammate_email`.
2. **Thread `--pair` flag** through `wgo today`, `wgo pr`, `wgo status`. The flag flips per-call; the config is the long-term default.
3. **`wgo team`**: new command that renders a two-column dashboard (you | teammate) using existing `internal/status/collector.go`. The teammate's branches are discovered via `gh search prs --author <teammate> --state open` plus `gh api search/issues` for recently-pushed branches.
4. **Plan rendering**: add an "Active With" section to plan markdown when at least one branch's spec frontmatter `authors:` includes both names.
5. **Spec frontmatter as the source of truth for "who is working on this"**: spec `authors:` list is canonical. wgo never invents pair attribution from git — only reads it from frontmatter.

## Inputs / Outputs / Contracts

### Config (`internal/config/config.go`)

```toml
[pair]
teammate        = "sujan-tcube"     # GitHub handle (required for pair features)
teammate_jira   = "sujan.tcube"     # optional, defaults to teammate
display_name    = "Sujan"           # shown in UI; defaults to teammate
teammate_email  = "sujan@virtru.com" # optional; used for git-author filtering in `today --pair`
```

```go
type PairConfig struct {
    Teammate      string `toml:"teammate"`
    TeammateJira  string `toml:"teammate_jira"`
    DisplayName   string `toml:"display_name"`
    TeammateEmail string `toml:"teammate_email"`
}
type Config struct {
    // existing fields...
    Pair PairConfig `toml:"pair"`
}
func (c *Config) HasPair() bool { return c.Pair.Teammate != "" }
```

### `wgo today --pair` (`internal/cmd/today.go`)

When `--pair` is set or `cfg.HasPair()`:

- Run today's existing collection twice: once for `cfg.Author`, once for `cfg.Pair.Teammate` (using `TeammateEmail` for git filtering, `Teammate` for `gh` filtering).
- Render two columns side-by-side, or stacked sections labeled `## Dave` and `## Sujan`.
- Add a `## Together` section listing branches/specs where both authors appear in spec frontmatter `authors:`.

### `wgo pr` Pair section (`internal/cmd/pr.go`)

Add a top-level `## Pair` section above existing sections. Lists:

- Sujan's open PRs (so Dave knows what's in flight)
- Sujan's PRs awaiting Dave's review
- Dave's PRs awaiting Sujan's review

Each PR row gains a spec column: `📄 WGO-101 (in_progress)` parsed by reading frontmatter from the PR's branch via `gh api repos/{owner}/{repo}/contents/spec/{ticket}.md?ref={branch}`.

### `wgo team` (new, `internal/cmd/team.go`)

```
$ wgo team
                Dave (dmihalcik)                |  Sujan (sujan-tcube)
─────────────────────────────────────────────────────────────────────────────────
 ◐ virtru/wgo       WGO-101-spec-scaffold       |  ◐ virtru/wgo  WGO-103-pair
   📄 spec/WGO-101.md (in_progress)             |     📄 spec/WGO-103.md (draft)
   3 commits ahead, PR #142 (review requested)  |     no PR yet
                                                |
 ● virtru/wgo       main                        |  ● virtru/wgo  main
─────────────────────────────────────────────────────────────────────────────────
 Together: WGO-101 (you), WGO-103 (Sujan), WGO-104 (drafting)
```

Implementation: extend `internal/status/collector.go`'s `Collect` to take an optional `author string` for remote branch enumeration. For local: walk Dave's discovered repos. For Sujan: `gh api graphql` for recently-pushed branches by author, then read spec frontmatter for status.

### Plan "Active With" section (`internal/plan/plan.go`)

When parsing, look for a `## Active With <name>` section formatted like `## Active Branches`. When rendering, walk all known specs (cached via `Annotation.SpecPath`), check frontmatter `authors:`, emit branches where `len(authors) > 1` and both `cfg.Author` and `cfg.Pair.Teammate` are members.

```markdown
## Active With Sujan
- **virtru/wgo:WGO-101-spec-scaffold** — Spec scaffold and plan integration 📄 spec/WGO-101.md
```

## Edge Cases & Constraints

- **No pair configured**: `--pair` flag errors with `pair not configured: set [pair] teammate in ~/.wgo/config.toml`. Default behavior of every command is unchanged.
- **`wgo team` performance**: GitHub calls per teammate could be slow. Cache GraphQL response with TTL of 60s; share the existing TTL cache used by `gh` wrappers.
- **Sujan's account is private / deactivated**: gracefully degrade — show "could not fetch teammate activity" and continue with Dave's column.
- **GitHub handle vs Jira handle vs git author email**: never reconcile silently. `cfg.Pair.Teammate` is GitHub handle. For commit attribution, use `Pair.TeammateEmail` if set; else use `Pair.Teammate` only for `gh` queries and skip git-log filtering.
- **Cross-repo pair work**: spec frontmatter `branches:` contains entries from multiple repos. "Active With" walks all of them.
- **Stale teammate cache**: `wgo team --refresh` bypasses the TTL cache.
- **Privacy**: never log Sujan's private repos. Honor `gh`'s default scope.
- **Display name fallback**: if `display_name` is empty, fall back to `teammate`. Never show a blank name in UI.

## Out of Scope

- More than 2-person teams. `[pair]` is exactly one teammate. Multi-person teams can be a follow-on if the pilot widens.
- Real-time presence ("Sujan is editing right now"). Polling-based GitHub view only.
- Syncing `~/.wgo/state.json` between machines. The shared surface is the spec file in the repo + GitHub.
- Slack/email notifications.
- Retroactively populating `authors:` on existing specs from git blame.
- A reverse-pair view (e.g., what would Sujan see on his machine) — each engineer configures their own `[pair]`.

## Acceptance Criteria

- [ ] `[pair] teammate = "sujan-tcube"` in `~/.wgo/config.toml` enables pair features
- [ ] `wgo today --pair` produces a single document with both engineers' commits, PRs, reviews, and spec edits
- [ ] `wgo today --pair --json` emits structured output usable for the WGO-104 pilot summary
- [ ] `wgo pr` (with pair configured) shows a `## Pair` section listing Sujan's open PRs and Dave's review requests
- [ ] Each PR row in `wgo pr` displays the spec link + status when the branch has a spec
- [ ] `wgo team` renders a side-by-side dashboard within ~3s on a fresh cache; `--refresh` bypasses cache
- [ ] When both `dmihalcik` and `sujan-tcube` are in a spec's frontmatter `authors:`, the branch appears under "Active With Sujan" in `wgo plan`
- [ ] All pair commands degrade gracefully and emit a clear hint when `[pair]` is unconfigured
- [ ] No GitHub call is made for pair features when `[pair]` is unconfigured (verified by integration test with mocked `gh`)
