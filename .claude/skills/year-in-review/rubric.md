# Year-in-review rubric

Every `yir-*` agent scores against this file, so a slice analysed today and one analysed next week land on the same scale. The scripts compute the **mechanical** part: bands, outcomes, and counts. Agents only apply the **judgement** adjustments described here, and they must show what they changed.

## Evidence rules (apply to everything)

1. Every claim cites at least one URL taken from the ledger, slice, card, or digest. That means a PR, ticket, review, or cross-reference.
2. Numbers are copied from the data, never estimated. If a number isn't in the data, leave it out.
3. If there is no evidence either way, write **"no signal"**. Don't infer impact from size, title, or seniority.
4. Separate *what happened* (merged, resolved, referenced) from *what it means* (enabled, unblocked). The meaning needs its own evidence: a downstream PR, a ticket that depended on it, or a reviewer's comment.
5. Bots (`*[bot]`, `coderabbitai`, `*-automation`, dependabot, renovate) are never collaborators and never count as downstream impact.

## Outcome (per work item)

| Outcome | Meaning |
|---|---|
| `shipped` | Merged or resolved Done, with no follow-up fix of yours pointing back at it after the merge |
| `shipped-with-rework` | Merged, but a later *merged* fix of yours referenced it, or a linked PR in the item was reverted |
| `reverted` | A merged `Revert …` PR references it |
| `in-flight` | Open and less than 30 days old at the end of the period |
| `stalled` | Open and 30 days old or more at the end of the period |
| `abandoned` | Closed without merging, or resolved as Won't Do / Duplicate |
| `filed` | A Jira ticket you reported but haven't worked on yourself (not assigned-and-resolved, and no status change by you): triage, planning, or backlog-shaping, for others or for your own future work |

**Agents may change an outcome only with evidence.** Two examples:
- A "stalled" PR superseded by a merged PR in the same item becomes `shipped`. Cite the superseding PR.
- An "abandoned" spike whose findings went into a spec or ticket becomes `shipped (exploratory)`. Cite where the findings went.

Text fields that end in `…[truncated]` were cut off. If the missing part matters to a judgement, read the full source with the read-only commands your agent file allows.

## Complexity

The script's score per PR is the sum of these points:

| Driver | Points |
|---|---|
| Lines changed ≥50 / ≥250 / ≥1000 | +1 / +2 / +3 |
| Files ≥5 / ≥15 / ≥40 | +1 / +2 / +3 |
| Human review threads + 2×changes-requested, ≥3 / ≥10 | +1 / +2 |
| More than 10 commits | +1 |
| Stacked on a non-trunk base | +1 |
| Story points ≥5 / ≥8 | +1 / +2 |
| Dependency bump | capped at 2 |

Bands: **L** is 0–2 points, **M** is 3–4, **H** is 5–6, and **XL** is 7 or more.

An item takes the highest band among its PRs. It moves up one band when it has 4 or more PRs or spans 2 or more repos. It is raised to at least H when it has 8 or more story points.

**Judgement adjustments.** Move an item at most one band, and write down the reason:
- **Down:**
  - Most of the churn is generated code, vendored files, lockfiles, golden or test fixtures, or mechanical renames
  - Large but copy-paste
  - A docs-only bulk edit
- **Up:**
  - A small diff in a hard domain: crypto, protocol or wire formats, concurrency, security boundaries, data migrations, or public API/ABI contracts
  - Coordination across teams or orgs
  - A novel design with a spec or ADR behind it
  - A tricky production incident
- **Never adjust** because of the repo's or ticket's prestige.

## Ratings (1–10), used by the lens agents

Anchor points: **2** means little or no signal, **5** means solid and typical for a senior engineer, **8** means clearly exceptional with evidence, and **10** means exceptional and corroborated by several independent signals. Always show the 2–4 facts behind a score.

| Rating | Measures | Main evidence |
|---|---|---|
| **Delivery** | how much planned work landed | share of work items shipped, cycle times, share of Jira resolved Done out of work assigned to you |
| **Quality** | whether it stayed landed | reverts, follow-up fixes, and changes-requested rounds relative to size |
| **Leverage** | whether others built on it | `downstream_others`, other people's PRs and tickets that reference yours, reusable tooling, docs, and specs |
| **Breadth** | range of collaboration | distinct human collaborators, teams outside your own, orgs, repos |
| **Depth** | substance of collaboration | review comments per review, the heaviest reviews, repeat partnerships |
| **Reciprocity** | balance of reviewing | reviews given vs received. Roughly 1:1 or better is healthy; a big deficit is a growth note, not a failing |

## Themes

- A theme is a **why**, not a repo. Good examples: "Large-file TDF security & ZIP64 support" and "Post-quantum KEM in DSP". Bad examples: "opentdf/platform work" and "misc fixes".
- Name themes in 3–7 words. The final report has 4–8 themes plus **KTLO / Support**. KTLO covers dependency bumps, CI fixes, reviews-only work, and triage.
- Items can belong to only one theme. When an item fits two, put it where it had the most effect.

## Tiers (in the final report)

| Tier | Contents |
|---|---|
| **Headline wins** | H/XL items that shipped, plus any item with leverage evidence |
| **Solid delivery** | M items that shipped, and groups of L items that together close out an epic |
| **KTLO & support** | reviews, bumps, CI, triage (`filed`) |
| **Stalled / learning** | stalled, abandoned, and reverted work, each with what was learned or why it stopped (only if there's evidence) |
