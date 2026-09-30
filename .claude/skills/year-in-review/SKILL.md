---
name: year-in-review
description: Review a developer's GitHub + Jira contributions over a period (year, half, quarter, or date range) and write an evidence-backed report grouped by theme, effectiveness, collaboration, and complexity. Use when the user asks to review their work, write a self-review / perf review / brag doc, summarize contributions for a period, or asks "what did I do in 2026 / H1 / Q3". Fans out to yir-* analyst agents.
argument-hint: "[period: 2026 | 2026-H1 | 2026-Q3 | A..B] [themes|collab|impact|complexity|gaps|all] [focus:<text>] [sequential] [refresh]"
allowed-tools: Bash, Read, Write, Grep, Glob, Agent
---

# Year in review

This skill reviews the contributions for a period and writes an evidence-backed report. It uses a map → lens → reduce → audit pipeline, the same shape as `pr-review-toolkit:review-pr` with a map step in front of it.
- **Scripts** do all the mechanical work: fetching, joining, metrics, and complexity scores.
- **Agents** make the judgement calls, each reading only a small file.
- **The orchestrator** (you) reads only indexes, summaries, and agent outputs. It never reads the raw ledger.

**Arguments:** "$ARGUMENTS"

Paths used below:
- `SKILL_DIR`: `.claude/skills/year-in-review`
- `RUBRIC`: `SKILL_DIR/rubric.md`
- `RUN`: the `run_dir` that `collect.py` prints

## 0. Parse arguments

| Argument | Values | Default |
|---|---|---|
| Period | `YYYY`, `YYYY-H1`/`H2`, `YYYY-Q1`–`Q4`, or `A..B` | the current year (`collect.py` stops it at today) |
| Aspects | `themes`, `collab`, `impact`, `complexity`, `gaps`, `all` | `all` |
| `focus:<text>` | deep-dive on one theme, feature, repo, epic, or lens | none |
| `sequential` | run agents one at a time | parallel |
| `refresh` | ignore month caches | use caches |

With `focus:` and no other aspects, run only the focus path (§4b).

## 1. Collect (no LLM)

```bash
python3 .claude/skills/year-in-review/collect.py --period <P> [--refresh] [--jira-projects X,Y]
```

- It prints the `run_dir` and counts. Progress goes to stderr.
- The first run for a year takes a few minutes: GitHub search is limited to 30 requests a minute, and each Jira issue needs its own view call.
- Closed months are cached under `~/.wgo/cache/review/raw`, so later runs only fetch the current month.
- Run it in the background if the period is longer than a quarter.
- If it exits non-zero, show the error and the fix. Typical fixes:
  - `gh auth status`
  - `acli jira auth login`
  - `--sources github`, to skip Jira

## 2. Slice (no LLM)

```bash
python3 .claude/skills/year-in-review/slice.py <RUN>
```

- This writes:
  - `RUN/slices/*.json`
  - `RUN/slices/index.json`
  - `RUN/reviews/<org>.json`
  - `RUN/stats.json`
- Read `index.json` and `stats.json` only. Show the user a compact table: slice id, items, PRs, Jira issues, and weight. Add a one-line headline built from the stats.
- If `coverage.json` has `dropped_ticket_prefixes` that appear often and look like real Jira projects, ask whether to re-run step 1 with `--jira-projects`.

## 3. Decide which agents to run

Follow review-pr's pattern: pick only the agents that apply.

| Agent | When |
|---|---|
| `yir-slice-analyst` | always, unless the aspects are only `collab` or `gaps` |
| `yir-gap-hunter` | always (cheap, haiku) |
| `yir-collaboration-lens` | aspects include `collab`/`all` and `stats.collaborators` ≥ 5 |
| `yir-impact-lens` | aspects include `impact`/`complexity`/`all`; needs cards |
| `yir-evidence-auditor` | whenever a report is drafted |
| `yir-deep-dive` | `focus:` given, or offered afterwards for the top 2 themes |

**Card cache.** Skip any slice whose `RUN/cards/<id>.json` already exists with the same `hash` as in `index.json`. On a rerun, only the slices that changed get analysed again.

**Batching.** Keep to **12 or fewer slice-analyst agents** in total. Put heavy slices (the top weights) in an agent of their own. Group light slices 2–3 per agent, keeping each batch under about 150KB of slice files combined.

## 4a. Map, then lenses

1. **Slice analysts.** In one message, launch every `yir-slice-analyst` agent at once (or one at a time with `sequential`). Each prompt contains:
   - the absolute slice paths
   - the absolute `RUBRIC` path
   - `RUN`
   - the instruction "write cards/<id>.json per slice; reply briefly"
2. **Lenses.** In a second message, launch the lenses that apply, again with absolute paths to `RUN` and `RUBRIC`:
   - `yir-impact-lens`
   - `yir-collaboration-lens`
   - `yir-gap-hunter`
   - The gap hunter needs no cards, so it can also go out with the analysts in the first message.
3. Each agent writes its own JSON file. You then read:
   - `cards/*.json`: only the `theme_candidates`, `story`, and `notable` fields. Use `jq`; don't read whole cards into context.
   - `lenses/*.json`

## 4b. Focus path

```bash
python3 .claude/skills/year-in-review/slice.py <RUN> --focus "<term>, <synonym>, <TICKET-1>, <repo>"
```

- Matching is literal keywords, so expand the user's focus into 3–6 comma-separated terms first. A term matches when all of its words appear.
  - For example, `focus:post-quantum` becomes `post-quantum, pqc, ml-kem, kem, quantum`.
  - Grep the slice index or `jq` the ledger titles to find the matching epic keys or repos, and add those as terms too.
- If fewer than 3 items match, show the closest slice ids and ask the user to rephrase.
- Otherwise, launch `yir-deep-dive` with the focus file, `RUN`, and `RUBRIC`. It writes `RUN/deep-dives/<slug>.md`.
- In a focus-only run, audit that file (§6) and stop.

## 5. Reduce themes and draft the report

1. **Merge themes.** Combine the cards' `theme_candidates` into **4–8 themes plus KTLO / Support**, following the rubric's theme rules. Record which slices went into each theme. If two candidates name the same *why*, they are one theme.
2. **Draft `RUN/draft.md`** using the structure below. Every item links with `[title](url)` so terminals render it as an OSC8 link.

```markdown
# <Period> in review — <github login>

## Headline
<3–5 bullets from stats.json: PRs merged, work items shipped by band, Jira resolved/SP, reviews given, collaborators, repos/orgs>

## Themes
### <Theme> — <one-line why>
<story, 3–6 sentences, merged from the slice stories>
- **Effectiveness:** <outcomes for this theme's items; notable rework>
- **Complexity:** <band mix; hardest item and its drivers>
- **Collaborators:** <names/teams from the cards and the collaboration lens>
- Key items: <3–6 links with a one-line what/why>

## Tiers
### Headline wins  ### Solid delivery  ### KTLO & support  ### Stalled / learning
(the rubric's tier rules; each entry is one line with links)

## Complexity × effectiveness
<the impact lens matrix as a table, with example links per cell>

## Collaboration
<ratings with facts, top partners by team, reach, mentoring signals, notable reviews>

## Effectiveness ratings
<delivery / quality / leverage with facts>

## Brag doc
<8–12 "did X → enabled Y → evidence Z" bullets taken from the card notables and the impact top 10>

## Growth & gaps
<stalled themes, reciprocity notes, rework lessons, and questions from the gap hunter>

## Data caveats
<source errors, truncation, unlinked PRs, quiet months, Confluence pages found (listed, not analysed)>
```

## 6. Audit, then finalise

1. Launch `yir-evidence-auditor` on `RUN/draft.md`.
2. Apply every `cut` and `rewrite` from `lenses/audit.json` exactly as given. Apply the `understated` upgrades where the evidence holds.
3. Write the final report to `~/.wgo/reviews/<label>.md`, where `<label>` is the basename of `RUN`. Ask before writing anywhere else.
4. Reply to the user with:
   - the report path
   - the headline bullets
   - the top 5 brag bullets
   - the gap hunter's questions for the user
   - an offer of `focus:<theme>` deep dives for the top 2 themes

## Guardrails

- **Read-only** everywhere except `~/.wgo/cache/review` and `~/.wgo/reviews`. Never comment, transition, push, or edit PRs or tickets.
- **No claim without a URL from the ledger.** The auditor enforces this. Don't bypass it.
- **Keep your own context small.** Never `cat` `ledger.jsonl` or whole slice files. Use `jq` for specific fields.
- If a data source is missing (no `acli`, or no Atlassian access), carry on with what's left and say so under Data caveats.
- The review is about the user's work. Mention collaborators by login only where the data shows the interaction. Never rate other people.
