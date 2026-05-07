---
ticket: WGO-201
title: Tiered specs and regenerable implementation plans
status: draft
authors: [dmihalcik, sujan]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
phase: 5
estimate: 3d
depends_on: [WGO-101, WGO-102]
---

# WGO-201 — Tiered specs and regenerable implementation plans

## You can assume (after WGO-101 + WGO-102)

- `internal/spec` exists with `Parse`, `RenderTemplate`, `FindByTicket`, `FindByBranch`, `ParseTicketFromBranch`, `UpdateFrontmatter`.
- `wgo add` scaffolds `spec/<TICKET>.md` from the WGO-101 template.
- `wgo spec {new,show,edit,ls,status,link}` exists and writes a single-file spec.
- `BranchEntry.SpecPath` and `Annotation.SpecPath/SpecState` round-trip through plan and state.

This ticket modifies the file-layout assumption (`<TICKET>.md` becomes the contract; a sibling plan file holds implementation detail) and extends the `wgo spec` command surface; it does not invalidate WGO-101's data model.

## Summary

Split the single-file spec into two artifacts: a stable, human-readable contract (`<TICKET>.md`) and one or more regenerable, agent-facing implementation plans (`<TICKET>.plan.md` or `<TICKET>/plan-N.md`). Add `wgo spec replan` to refresh plans against current code, `spec/CLAUDE.md` for cross-cutting agent guidance, a stale-plan hook, and a Claude Code skill that sequences spec → plan → execute.

The goal is **fidelity** (humans read a stable contract; agents read a current map of the code) and **performance** (small-context agents stop burning tokens rediscovering what the spec could have told them).

## Problem / Motivation

The WGO-101 template tries to serve two audiences with conflicting needs:

- **Human reviewers and teammates** want a stable contract describing *what* and *why*. Line numbers, file maps, "you can assume," and step-by-step commit staircases are noise to skim past.
- **Small-context agents** (Sonnet, Haiku, sub-agents with 16–32K usable context) need *pre-resolved* file maps and explicit guardrails so they don't spend their context budget rediscovering structure that the spec already knew.

Stuffing both into one file makes specs **brittle** (hardcoded line numbers rot the moment the file is edited) and **verbose** (humans skim past detail meant for agents). Splitting them lets each audience read what they need at the right TTL: the spec is the slow-changing contract committed for the life of the work; the plan is short-lived working memory regenerated whenever the code drifts.

A second motivation: PR-stacked work needs **multiple plans against one spec**, with a clear dependency chain (plan-2 assumes plan-1 has merged). The current single-file model has nowhere to put this.

A third motivation: cross-cutting agent guidance ("don't refactor adjacent code," "stop when AC pass + `go vet` is clean," "regenerate stale plans before reading") doesn't belong in any individual spec. It belongs in `spec/CLAUDE.md` and is loaded automatically when an agent works in the spec directory.

## Proposed Solution

1. **Two-tier file layout.** Sidecar by default; promote to a directory when work stacks.

   ```
   spec/
     CLAUDE.md                            # auto-loaded by Claude when in spec/
     WGO-101.md                           # spec — slow-changing contract
     WGO-101.plan.md                      # implementation plan — regenerable
     WGO-103/                             # promoted because work is PR-stacked
       spec.md
       plan-1.md
       plan-2.md                          # depends on plan-1 having merged
   ```

2. **`wgo spec new`** (extending WGO-102) writes both files: `<TICKET>.md` from the spec template and an empty `<TICKET>.plan.md` shell that says *"regenerate me with `wgo spec replan <TICKET>`."*

3. **`wgo spec replan <TICKET> [--plan NAME] [--new-plan]`** (new). Reads the spec contract, queries the current code (LSP, grep, optionally an LLM), regenerates the plan in place. Preserves human-edited regions via fenced markers. Stamps `generated_against` (commit SHA) and `generated_at` (timestamp) into the plan's frontmatter.

4. **Plan template** distinct from the spec template. Sections: *You can assume* (what prior plans/PRs landed), *Files to read* / *Files to create* / *Files to modify*, *Worked examples* (input → output deltas), *Commit staircase*, *Verification*, *Open questions during implementation*. No prose; agent-oriented.

5. **`spec/CLAUDE.md`** as the cross-cutting agent operating manual. Loaded automatically when an agent enters `/spec/`. Holds the things that don't belong in a spec template: stop conditions, error-wrapping patterns, "don't refactor adjacent code," build/test/lint commands, and the rule "regenerate the plan if `generated_against` is more than N commits behind HEAD."

6. **Stale-plan hook.** A pre-tool-use hook that fires when an agent reads any file matching `spec/**/*plan*.md`. Reads the plan's frontmatter, compares `generated_against` to HEAD, and if drift exceeds a threshold (default: 5 commits or 7 days), warns the agent and suggests `wgo spec replan`.

7. **Claude Code skill: `spec-driven-dev`.** Sequences the pipeline:
   - `/spec-driven-dev new TICKET "title"` → Opus drafts `<TICKET>.md`; human iterates.
   - `/spec-driven-dev plan TICKET [--plan NAME]` → Opus fills the plan from the current spec + code; human iterates.
   - `/spec-driven-dev replan TICKET` → wraps `wgo spec replan`.
   - `/spec-driven-dev execute TICKET [--plan NAME]` → spawns a sub-agent with only the plan + `spec/CLAUDE.md` + the file map; sub-agent stops when verification passes.

   Skill lives at `~/.claude/skills/spec-driven-dev/` (per-user); a starter version is committed under `spec/.claude-skill/` so a new pair member can install it locally with `wgo spec install-skill`.

## Inputs / Outputs / Contracts

### File layout decision rules

- **Default**: sidecar layout — `spec/<TICKET>.md` + `spec/<TICKET>.plan.md`.
- **Stacked**: directory layout — `spec/<TICKET>/spec.md` + `spec/<TICKET>/plan-1.md` + `plan-2.md` ...
- **Promotion**: automatic the first time `wgo spec replan <TICKET> --new-plan` is invoked. The single sidecar plan becomes `plan-1.md`; the new plan becomes `plan-2.md`. The spec is moved to `<TICKET>/spec.md`. A symlink may be left at `<TICKET>.md` for backwards compatibility (configurable).
- **Locator** (`internal/spec/locate.go`) tries sidecar first, then directory, returning the first match.

### Plan template (`internal/spec/default_plan_template.md`)

```markdown
---
spec: WGO-101.md
generated_against: <commit-sha>
generated_at: <timestamp>
generated_by: claude-opus-4-7
depends_on_plans: []                       # for stacked work
preserved_sections: [notes, decisions]
---

# Plan: <ticket title>

## You can assume
<auto-filled from spec frontmatter `depends_on` + transitive plan history>

## Files to read (do not modify)
- `path/to/file.go` — `FunctionName` (lines L-L)

## Files to create
- `internal/spec/foo.go` — exports `Foo`, `Bar`

## Files to modify
- `internal/cmd/add.go` — extend `addWithWorktree` to ...

## Worked examples
<concrete input → output deltas, shell sessions>

## Commit staircase
1. `<scope>: ...`
2. ...

## Verification
| AC | Command | Expected |
|---|---|---|

<!-- preserved:notes -->
<!-- /preserved -->

## Open questions during implementation
<!-- preserved:open-questions -->
<!-- /preserved -->
```

Line numbers in `Files to read` / `Files to modify` are **derived**, never authored. `wgo spec replan` regenerates them.

### `wgo spec` surface changes

| Command | Behavior |
|---|---|
| `wgo spec new <TICKET> ["title"]` | Writes spec.md + empty plan.md sidecar |
| `wgo spec replan <TICKET> [--plan NAME]` | In-place plan regeneration; preserves fenced sections; updates `generated_against` |
| `wgo spec replan <TICKET> --new-plan [NAME]` | Creates next plan in stack; promotes to directory layout if needed |
| `wgo spec execute <TICKET> [--plan NAME]` | Convenience wrapper invoking the Claude Code skill |
| `wgo spec install-skill` | Copies `spec/.claude-skill/` into `~/.claude/skills/` |

`wgo spec ls` (from WGO-102) gains a `Plans` column showing plan count and freshness glyph (`✓` fresh, `↯` stale, `✗` missing).

### Preserved-section markers

```markdown
<!-- preserved:NAME -->
human-edited content the agent must keep
<!-- /preserved -->
```

`replan` walks the existing plan, extracts every `preserved:NAME` block, regenerates the rest, then re-injects preserved blocks at the same anchors. Names listed in frontmatter `preserved_sections:` are required to round-trip; unknown preserved names are kept best-effort and warned about.

### `spec/CLAUDE.md` content sketch

```markdown
# Working in /spec/

## You are reading or editing a spec or implementation plan.

- Specs (`<TICKET>.md` or `<TICKET>/spec.md`) are the contract. Edit them when scope or AC changes; don't add implementation detail here.
- Plans (`<TICKET>.plan.md` or `<TICKET>/plan-*.md`) are short-lived working memory. They are regenerated by `wgo spec replan`.

## Before reading any plan
If the plan's `generated_against` commit is more than 5 commits behind HEAD, run `wgo spec replan <TICKET>` first. Stale line numbers will mislead.

## Stop conditions for implementation
You are done when, in this order:
1. Every AC verification command exits zero.
2. `go vet ./...` is clean.
3. `go test ./...` passes.
4. `gofmt -l .` produces no output.

Do not "improve" beyond that. Do not refactor adjacent code.

## Error wrapping
Use `fmt.Errorf("context: %w", err)`. Don't add error handling for paths that cannot fail (internal calls, framework guarantees).

## Don't
- Don't add comments that explain *what* the code does.
- Don't introduce new dependencies without saying so in the plan.
- Don't `git commit --no-verify`.
```

### Stale-plan hook contract

A `PreToolUse` hook in `~/.claude/settings.json` (installable via `wgo spec install-hook`):

- Triggers on `Read` whose `file_path` matches `**/spec/**/plan*.md` or `**/spec/*.plan.md`.
- Parses YAML frontmatter from the target.
- If `generated_against` ∉ `git log --pretty=%H HEAD~<threshold>..HEAD`, emits a non-blocking warning naming the staleness and the regen command.
- Configurable threshold (commit count or age): `[hooks] plan_stale_commits = 5`, `plan_stale_days = 7`.

The hook is non-blocking by default. An opt-in `block` mode refuses the read until regen, for users who want hard correctness.

### Claude Code skill contract (`spec-driven-dev`)

Skill manifest highlights:
- Triggers on `/spec-driven-dev` and on user messages mentioning "spec" + a ticket ID.
- `new` step: prompts Opus with the pilot rules + the spec template + the user's title/desc; iterates with the human until they're happy.
- `plan` step: invokes `wgo spec replan` under the hood, then opens the result for human review.
- `execute` step: spawns a sub-agent (Sonnet by default) with system prompt = `spec/CLAUDE.md` + user message = the plan file. Sub-agent has tool access scoped to the file map only.

## PR stack

This work ships as four PRs to keep review surface small:

| PR | Scope | Verification |
|---|---|---|
| **PR 1**: layout + CLAUDE.md | New `spec/CLAUDE.md`, locator handles both sidecar and directory layouts, existing WGO-101..WGO-104 specs stay put (sidecar). | `wgo spec ls` lists all five tickets; `spec/CLAUDE.md` is loaded by Claude when reading a spec (manually verified). |
| **PR 2**: `wgo spec replan` + plan template | New plan template, `wgo spec new` writes both files, `wgo spec replan` regenerates with preserved fences. | Smoke test below. |
| **PR 3**: stale-plan hook | Pre-tool-use hook script + `wgo spec install-hook` + config knobs. | Hook warns on a plan whose `generated_against` is 6 commits behind HEAD. |
| **PR 4**: Claude Code skill | Starter skill committed under `spec/.claude-skill/`; `wgo spec install-skill` copies into `~/.claude/skills/`. | `/spec-driven-dev new WGO-999 "demo"` produces a draft spec. |

Each PR has its own `plan-N.md` once we promote `WGO-201/` to a directory.

## Edge Cases & Constraints

- **Existing single-file specs (WGO-101..WGO-104)**: keep working. Locator falls back to single-file when no sidecar plan exists. `wgo spec replan` on a single-file spec creates the missing plan sidecar; does not modify the spec.
- **Symlink at `<TICKET>.md` after promotion**: opt-in via `[spec] keep_symlink_on_promote = true`. Default off — promotion moves cleanly into the directory.
- **Preserved-section conflicts**: if two preserved blocks share a name, error out and refuse to regenerate. The human resolves by renaming.
- **Plan regeneration without LLM access**: `wgo spec replan --no-llm` does the structural pass only (file existence, line lookup for symbol names that already appear in the plan). Useful for offline regeneration.
- **`generated_against` missing**: treat as fully stale; replan always offered.
- **Hook performance**: hook reads one frontmatter block (~1 KB) and runs one `git rev-parse`. Budget: <30ms p95.
- **Skill availability**: skill is optional. `wgo spec replan` and the rest work without Claude Code installed; the skill is sugar.
- **Branch with no spec**: `wgo spec replan` errors with a clear hint to run `wgo spec new <TICKET>` first.
- **Plan dependencies in a stack**: `plan-2.md`'s frontmatter `depends_on_plans: [plan-1.md]` is read by `replan` to populate "You can assume" automatically.
- **Multi-repo specs** (WGO-101 single-canonical-repo rule): plans live alongside the spec in the canonical repo. Other repos in the effort don't get plan files.

## Out of Scope

- LLM prompt quality for `replan` and the skill — tunable separately. This ticket ships the plumbing, not the prompt.
- Migrating other repos' specs to the new layout. Each repo opts in by running `wgo spec replan` on its existing specs.
- Auto-promoting sidecar to directory layout based on heuristics (commit count, branch count). Promotion stays explicit via `--new-plan`.
- Plan diffing / "what changed in the plan since I last read it." Possible follow-up.
- Replan via LSP for non-Go languages. Phase 1 is grep + Go LSP only; other languages get grep-only.
- A web view of the plan/spec relationship.
- Sub-agent token-budget enforcement — left to the skill's runtime.

## Open Questions

- **Skill home**: ship the skill in this repo under `spec/.claude-skill/` and `install` to `~/.claude/`, or publish it as a Claude Code plugin/marketplace entry? Repo-local is simpler for the pilot; marketplace is the path if it earns its keep.
- **Promotion threshold**: should sidecar→directory promotion happen on `--new-plan` only (current proposal), or also when the plan exceeds a size threshold? Lean toward explicit-only.
- **Symbolic anchor format in plan**: prefer `internal/cmd/add.go::addWithWorktree` (function-qualified) or `internal/cmd/add.go (line 103, addWithWorktree)` (line + symbol)? Function-qualified is regen-stable; line is more navigable. Decide before PR 2.
- **Hook block mode default**: warn-only vs block-by-default. Pilot starts warn-only; revisit after 2 weeks of usage data.
- **"Files to read"** in plans: should `replan` actually read those files itself and embed relevant snippets, or just point at them? Embedding burns more spec-time tokens but saves agent-time tokens. Ship as a flag (`--inline-snippets`) and measure.
- **Preserved-section markers**: HTML comments work everywhere but are ugly. Alternative: a YAML block at the bottom with named keys. Lean toward HTML comments for now (round-trip via markdown editors).

## Acceptance Criteria

| AC | Verification |
|---|---|
| `wgo spec new WGO-300 "demo"` produces a spec **and** an empty plan sidecar | `ls spec/WGO-300.md spec/WGO-300.plan.md` exits 0 |
| `wgo spec replan WGO-300` populates the plan and stamps `generated_against` to HEAD | `grep -q "generated_against: $(git rev-parse HEAD)" spec/WGO-300.plan.md` |
| `wgo spec replan WGO-300` preserves a fenced `<!-- preserved:notes -->` block edited by the human | manually edit the fence, replan, then `grep "$EDIT_MARKER" spec/WGO-300.plan.md` |
| `wgo spec replan WGO-300 --new-plan` promotes to directory layout | `[ -d spec/WGO-300 ] && [ -f spec/WGO-300/spec.md ] && [ -f spec/WGO-300/plan-1.md ] && [ -f spec/WGO-300/plan-2.md ]` |
| Locator finds either layout | `wgo spec show WGO-300` succeeds for both sidecar and directory layouts |
| `spec/CLAUDE.md` exists and is auto-loaded by Claude Code | open Claude Code in `spec/`, ask "what are the stop conditions?" — answer reflects CLAUDE.md content |
| Stale-plan hook warns when plan is N+ commits behind HEAD | install hook, regenerate plan, make 6 commits, then `Read` the plan via Claude — warning appears |
| Hook's frontmatter parse + git rev-parse runs in <30ms p95 | benchmark with `hyperfine 'cat spec/WGO-300.plan.md \| /path/to/hook'` |
| `wgo spec install-skill` copies the starter skill into `~/.claude/skills/spec-driven-dev/` | `ls ~/.claude/skills/spec-driven-dev/SKILL.md` exits 0 |
| `wgo spec ls` shows a Plans column with freshness glyph | `wgo spec ls --json | jq '.[] | .plans'` returns count + freshness fields |
| Old single-file specs (WGO-101..WGO-104) still work | `wgo spec show WGO-101` succeeds without modification |
| `wgo spec replan WGO-101` (a single-file spec) creates the missing plan sidecar | run command, then `[ -f spec/WGO-101.plan.md ]` |
| Plans without `generated_against` are treated as fully stale | hand-write a plan with no frontmatter, read via Claude with hook installed — warning appears |

## Verification (cumulative smoke test)

```bash
cd /Users/dmihalcik/Documents/GitHub/virtru/wgo
go build -o wgo ./cmd/wgo

# PR 1: layout + CLAUDE.md
ls spec/CLAUDE.md
./wgo spec ls                                 # all WGO-1xx specs visible

# PR 2: replan + plan template
./wgo spec new WGO-300 "demo"
test -f spec/WGO-300.md && test -f spec/WGO-300.plan.md
./wgo spec replan WGO-300
grep -q "generated_against: $(git rev-parse HEAD)" spec/WGO-300.plan.md

# Edit a preserved fence and confirm round-trip
echo "MY_NOTE_$$" >> spec/WGO-300.plan.md  # inside <!-- preserved:notes -->
./wgo spec replan WGO-300
grep -q "MY_NOTE_$$" spec/WGO-300.plan.md

# Promote to directory
./wgo spec replan WGO-300 --new-plan
test -d spec/WGO-300 && test -f spec/WGO-300/plan-2.md

# PR 3: stale-plan hook
./wgo spec install-hook
# (manual) make 6 unrelated commits, then read the plan via Claude — warning appears

# PR 4: skill
./wgo spec install-skill
test -f ~/.claude/skills/spec-driven-dev/SKILL.md
```
