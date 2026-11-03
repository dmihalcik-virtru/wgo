# <abbr title="what's going on">wgo</abbr>

Track development context across your computer.

Follow your branches, worktrees, PRs, and AI agent sessions across multiple repositories and streams of work.
Sync your current work in a coherent ~/.plan file.
Sync your work with your task planners, including Jira and GitHub Issues.
Enhance your parallel, concurrent workflows with a generalized stack of related commits (replacing rosie and graphite).

Some cool features include:

1. Configure a [shell alias](#shell-alias) to get `cd` to PR behavior. 
   ```sh
   wto https://github.com/your/repo/pull/4
   ```
2. Remember what you are last working on
   ```sh
   wgo status
   ```


## Make Sense of your Chaos

Developers with many branches, worktrees, and repos across multiple checkouts lose track of:

- **What** branches they created or are following
- **Why** those branches exist
- **Where** things are located on their filesystem
- **How** the different changes interact and are influenced by their spec and bug reports


## Features

### Current (Phases 1-3)

- **`wgo .`** — Instantly see your current repository context (branch, status, commits, remote tracking)
- **`wgo plan`** — View and manage a human-readable markdown plan file tracking all your work
- **`wgo plan add "reason"`** — Annotate branches with their purpose
- **`wgo plan effort`** — Group bookmarks across repositories into named [efforts](#efforts)
- **`wgo ls`** — List all repositories across your filesystem with their status
- **Persistent tracking** — All annotations stored in `~/.wgo/state.json`
- **Plan file** — Human-editable markdown at `~/.plan` (symlinked from `~/.wgo/plan.md`)
- **Fast discovery** — Automatically finds repositories in configured directories

### Jump to Any GitHub URL

- **`wgo to <url>`** — Paste a GitHub PR, branch, or issue URL and get a local worktree path back to go to
- Clones the repo if you don't have it, creates a worktree, and prints the path
- Works with `cd $(wgo to ...)` or the `wto` shell alias (see [Shell Alias](#shell-alias) below)

### Debug What Actually Shipped

- **`wgo rig new <name> --from-binary ./dsp`** — Check out every in-org module a released artifact
  depends on, each pinned to the commit it shipped from, wired together by a generated `go.work`
- **`wgo rig verify`** — Prove the rig still resolves the versions the artifact recorded
- See [Debugging a Shipped Artifact](#debugging-a-shipped-artifact-with-wgo-rig)

### Coming Soon

- GitHub PR integration with cached status
- Fuzzy finder for quick worktree/branch selection
- Effort grouping in `wgo status` and `wgo .` (efforts and workspace attribution exist; `wgo dash --json` uses them, `wgo status` and `wgo .` do not yet)

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/virtru/wgo.git
cd wgo

# Build the binary
go build -o wgo ./cmd/wgo

# Optionally install to your PATH
go install ./cmd/wgo
```

### Prerequisites

- Go 1.26 or later
- [`jj`](https://jj-vcs.github.io/jj/latest/install-and-setup/) (Jujutsu) 0.42 or later — required at runtime
- A `GITHUB_TOKEN` environment variable, **or** [GitHub CLI](https://cli.github.com/) (`gh`) installed for `gh auth token` to bootstrap credentials

`wgo` no longer shells out to the `git` CLI for any operation — workspace,
branch (bookmark), and remote management all go through `jj`. The GitHub
integration talks to the REST API directly; `gh` is only used to resolve
an API token when `GITHUB_TOKEN` is unset.

### Removed in the jj migration

The transition to jj also retired a handful of features that no longer
make sense (or that jj covers natively):

- **Passive git hooks** (`wgo hooks ...`). The hook system relied on
  `core.hooksPath` and tracked branch creation / commit events. jj's
  operation log already records the same information and `wgo doctor`
  surfaces drift on demand.
- **`wgo stack {new,push,restack,rm,adopt,sync}`** and `wgo stack status`.
  jj's commit DAG is the source of truth for stacks; `wgo sync`
  (top-level) handles PR-base alignment.
- **Cross-repo stacked PRs.** The per-repo DAG model in jj is the unit
  of work; multi-repo grouping happens via the spec file's `branches:`
  frontmatter and the `~/.plan` markdown, not via persistent stack state.

### No-migration policy for old state

If you have an existing `~/.wgo/state.json` from a pre-jj installation,
**delete it and re-run `wgo`**:

```bash
rm ~/.wgo/state.json
wgo ls   # repopulates state from on-disk discovery
```

The schema bumped from v1 to v2 (drops the `Stacks` / `Parents` / `StackID`
fields) and there is no in-place upgrade path.

### Tradeoffs of jj workspaces

The main checkout of a repo (the clone under `worktree.mains_dir`) is
colocated with Git by default (`jj git init --colocate` / `jj git clone
--colocate`), and `wgo` will retroactively colocate an existing main
checkout the first time it adds a workspace to it. This gives the main
checkout a real `.git/` directory so `git` commands, IDE plugins, and other
git-aware tooling work there directly.

Workspaces created by `wgo add` and `wgo to`, however, never get their own
`.git/` — this is a jj limitation, not a wgo choice: `jj git colocation
enable`/`disable` only operate on a repo's main workspace, and secondary
workspaces added via `jj workspace add` are always plain jj. External tools
that look for git state will not work inside those workspaces:

- IDE / editor git plugins that rely on libgit2 or `.git/index`
- The [`pre-commit`](https://pre-commit.com/) framework
- `direnv`'s git-aware features

If you need those tools, run them against the (now-colocated) main checkout
instead of a workspace, or upstream a jj-aware adapter.

### git-lfs in jj workspaces

jj never invokes git's clean/smudge filters, colocated or not, so files
tracked by [git-lfs](https://git-lfs.com/) stay as raw pointer text
(`version https://git-lfs.github.com/spec/v1...`) no matter how you check
them out with jj.

Running `git lfs checkout` yourself to fix this is risky: jj has no staging
area, so the hydrated (potentially huge) file content gets snapshotted
straight into your current change as ordinary content, defeating the point
of LFS if that change is ever described or pushed without being reverted
first.

`wgo lfs sync [path]` does this safely instead: it fetches the objects your
workspace needs into the main checkout's git-lfs cache (`git lfs fetch`,
which never touches a working tree) and replaces each pointer file with a
**symlink** into that cache. Reading the file works transparently for any
tool, but the diff jj sees is a tiny "regular file became symlink" change
instead of the full blob — much safer if a workspace is accidentally
committed or pushed while hydrated. Run `jj restore <path>` to revert a
hydrated path back to its pointer before committing or pushing it. `wgo lfs
status [path]` reports pointer vs. hydrated counts without changing
anything. Both are no-ops if `git`/`git-lfs` aren't installed.

## Usage

### Show Current Repository Context

```bash
wgo .
```

**Output:**
```
repo:   wgo
branch: feat/plan-parser
status: 3 modified, 1 staged
remote: ↑2 ↓0 (origin/feat/plan-parser)
commit: abc1234 Add initial plan parser (2 hours ago)
```

### Shell / Claude Code prompt

`wgo statusline` renders the same context as a single, colored, clickable line
built for prompts. It is **local-only**: PR status comes from the on-disk cache
(`~/.wgo/cache/`), so a warm render makes **zero** network calls and finishes in
well under 100ms. On a cache miss it simply omits the PR segment (and kicks a
background refresh) rather than blocking.

```bash
wgo statusline
# → wgo feat/plan-parser* [WGO-131] ↑2 #42 open
```

Branch, repo, and PR link to GitHub; the ticket links to Jira (or the GitHub
issue for `gh-<n>` branches). Flags:

- `--json` — emit the full context (same schema as `wgo . --json`).
- `--format '<tmpl>'` — Go `text/template` over the context. Funcs: `upper`,
  `lower`, `color "<name>" <s>`, `link "<url>" "<text>"`. Fields include
  `.Repo`, `.Branch`, `.Dirty`, `.Ticket`, `.Ahead`, `.Behind`, and `.PRs`
  (each with `.Number`, `.State`, `.URL`).
- `--no-color` — plain text (also honored via the `NO_COLOR` env var).
- `--refresh` — force a synchronous PR fetch and warm the cache (use in a
  background warmer, not the hot path).
- `--siblings` — include sibling workspaces (adds a filesystem walk).

Target another directory without `cd` using the global `-C/--repo` flag:

```bash
wgo -C /path/to/repo statusline
```

**Claude Code / shell integration.** `contrib/statusline.sh` reads the working
directory (from Claude Code's JSON on stdin, else `$PWD`) and calls
`wgo -C "$dir" statusline`:

```bash
# Claude Code: set this script as your statusLine command.
# zsh prompt:
precmd() { RPROMPT="$(wgo statusline)" }
# fish prompt:
function fish_right_prompt; wgo statusline; end
```

Keep the cache warm with a periodic refresh (cron or a shell hook):

```bash
wgo statusline --refresh >/dev/null 2>&1 &
```

**Agent sessions.** `wgo agent status` lists AI agent sessions across
workspaces; several agents can share one workspace. For Claude Code, wire up
`wgo agent hook` as described in
[`contrib/claude-code-hooks.md`](contrib/claude-code-hooks.md); other tools
can use `wgo agent start --json`, `heartbeat` and `stop --session`.

### Annotate Your Current Branch

```bash
wgo plan add "Fixing authentication token refresh bug"
```

This creates an annotation linking your current branch to its purpose.

### View Your Plan

```bash
wgo plan
```

**Output:**
```markdown
# Plan

## Active Branches

- **virtru/wgo:feat/plan-parser** — Add initial plan file parsing
- **virtru/api:fix/auth** — Fixing auth token refresh bug

## Notes

(Your custom notes here)
```

### Edit Your Plan

```bash
wgo plan edit
```

Opens `~/.plan` in your `$EDITOR` (defaults to `vi`). Manual edits are preserved through wgo operations.

wgo only rewrites the parts of the plan it manages (Tasks, Active Branches,
Efforts and Notes) and only the lines it was asked to change. Your own
sections, prose, code fences and line endings are written back exactly as you
left them, and anything it cannot interpret is kept and reported as a
`warning:` rather than dropped.

### Efforts

An *effort* groups the bookmarks that make up one piece of work across
repositories. Efforts live in the `## Efforts` section of the plan and in
`~/.wgo/state.json`; write them by hand or with `wgo plan effort`:

```markdown
## Efforts

### Rotate signing keys
Move the platform and SDK to the new key format.

- platform:gh-71-rotate
- opentdf/sdk:rotate-keys
```

```bash
wgo plan effort add "Rotate signing keys" -d "Move the platform and SDK to the new key format"
wgo plan effort link "Rotate signing keys" platform:gh-71-rotate
wgo plan effort unlink "Rotate signing keys" platform:gh-71-rotate
wgo plan effort remove "Rotate signing keys"
```

Each entry is `repo:bookmark`, where `repo` is a discovered main clone's
directory name, its `owner/repo`, or its absolute path. Commands write the
shortest of those that is unambiguous. A hand edit of the plan wins over the
state copy of the same effort, and the effort's ID comes from its heading, so
renaming a heading creates a new effort.

`remove` will not delete hand-written notes or malformed entries under an
effort without `--force`; it lists them first.

Workspaces are attributed to an effort by their current bookmark in their
repository, never by bookmark name alone, so the same name in two repos does not
collide. A workspace that no effort claims is grouped by its ticket ID, then
left ungrouped; one claimed by several efforts is reported as a conflict.

### List All Repositories

```bash
wgo ls
```

**Output:**
```
REPO                 BRANCH               WHY                              STATUS
---------------------------------------------------------------------------------------
wgo                  main                 Testing phase 2 implementation   2M 7U
gwq                  main                 —                                clean
workset              main                 —                                clean
virtru/platform      main                 —                                1M
```

Status codes:
- `M` = Modified files
- `A` = Added files
- `D` = Deleted files
- `U` = Untracked files
- `clean` = No changes

### Jump to a GitHub URL

```bash
wgo to https://github.com/owner/repo/pull/42
```

Given a GitHub URL, `wgo to` resolves it to a local worktree and prints the path to stdout. It handles PRs, branches, and issues:

```bash
# PR — looks up the head branch via gh, creates a worktree
wgo to https://github.com/virtru/platform/pull/123

# Branch — creates a worktree for the branch
wgo to https://github.com/virtru/platform/tree/feature/auth

# Issue — creates a new branch named "42-issue-title-slug"
wgo to https://github.com/virtru/platform/issues/42
```

**The trunk lives in `mains_dir`, everything else in `worktrees_dir`.** A bare repo URL or an explicit trunk URL resolves to the clone itself — that clone already *is* the trunk checkout, so there is no second copy of it:

```bash
wgo to https://github.com/virtru/platform.git
wgo to https://github.com/virtru/platform/tree/main
# → ~/Documents/GitHub/mains/virtru/platform

wgo to https://github.com/virtru/platform/tree/feature/auth
# → ~/Documents/GitHub/worktrees/feature-auth/platform
```

If the repo isn't cloned locally, `wgo to` clones it first. If a worktree for the branch already exists, it returns the existing path.

If the main clone has in-progress work on it, `wgo to` still prints the clone's path and warns on stderr — it never moves your work. Use [`wgo park`](#park-work-from-a-main-clone) when you want it relocated.

Progress messages go to stderr, so you can wrap it with `cd`:

```bash
cd $(wgo to https://github.com/virtru/platform/pull/123)
```

Requires the [GitHub CLI](https://cli.github.com/) (`gh`) for PR and issue lookups.

### Shell Completion

`wgo` supports tab completion for all commands via the built-in `completion` subcommand.

#### Zsh

```zsh
# Add to ~/.zshrc
eval "$(wgo completion zsh)"
```

Or install to a file (faster shell startup):

```zsh
wgo completion zsh > "${fpath[1]}/_wgo"
```

#### Bash

```bash
# Add to ~/.bashrc or ~/.bash_profile
eval "$(wgo completion bash)"
```

#### Fish

```fish
# Run once to persist:
wgo completion fish > ~/.config/fish/completions/wgo.fish
```

### Shell Alias

For a faster workflow, add a `wto` function to your shell config (`~/.zshrc` or `~/.bashrc`):

```zsh
# wto — cd into a GitHub URL's local worktree
wto() {
  local dir
  dir=$(wgo to "$@") && cd "$dir"
}

# Tab completion for wto (delegates to wgo to)
compdef _wgo_to wto
_wgo_to() {
  compadd ${(f)"$(wgo __complete to -- "${words[2,-1]}" 2>/dev/null | grep -v '^:' | cut -f1)"}
}
```

Then just:

```bash
wto https://github.com/virtru/platform/pull/123
# you're now in the worktree
```

### Track a Repository

```bash
wgo track .           # Track current directory
wgo track /path/to/repo  # Track specific path
```

Manually registers a repository for tracking in `~/.wgo/state.json`.

### Park work from a main clone

A clone under `mains_dir` is the trunk checkout; wgo expects its `@` to be a clean empty change on trunk. When you start editing there instead of in a worktree, `wgo park` moves that work where it belongs:

```bash
cd ~/Documents/GitHub/mains/virtru/platform

wgo park --dry-run
# @ in ~/Documents/GitHub/mains/virtru/platform is not on main
#   2 change(s), 3 uncommitted file(s), no bookmark
#   qpvuntsm fix: handle expired refresh tokens
#   zzzzzzzz (no description)
#   will create bookmark fix-handle-expired-refresh-tokens
#   destination: ~/Documents/GitHub/worktrees/fix-handle-expired-refresh-tokens/platform
# dry run: nothing was changed

cd $(wgo park)
```

Nothing is copied — jj workspaces share one repo store, so the work is claimed by a new workspace and the clone's `@` returns to trunk. The destination path is the only thing on stdout, so `cd $(wgo park)` works.

The slug comes from an existing bookmark if the work has one, otherwise from the newest commit description, otherwise `wip-<change-id>`. Override it with `--name`. A bookmark is created by default so `wgo .`, `wgo status`, and `wgo sync` can see the work; `--no-bookmark` opts out.

Every check runs before the first mutation, so a rejected park leaves the repo untouched, and any failure mid-move rolls back and tells you the `jj edit` that recovers your work.

## Configuration

wgo automatically creates `~/.wgo/config.toml` on first run with sensible defaults:

```toml
[discovery]
# Base directories to scan for repositories
base_dirs = ["/Users/you/Documents/GitHub"]

# Maximum depth to scan (0 = unlimited)
scan_depth = 4

# Patterns to exclude from discovery
exclude_patterns = ["node_modules", ".cache", "vendor", "dist"]

[worktree]
# Where trunk checkouts live: <mains_dir>/<owner>/<repo>
mains_dir = "/Users/you/Documents/GitHub/mains"

# Where everything else lives: <worktrees_dir>/<slug>/<repo>
worktrees_dir = "/Users/you/Documents/GitHub/worktrees"

[rig]
# Where rigs are created: <dir>/<name>
dir = "/Users/you/Documents/GitHub/rigs"

# Module path prefixes to check out from source; everything else is left to the
# module cache. Required — an empty list admits no dependency at all, so
# `wgo rig new` rejects it rather than building a rig holding only the primary
# module. Override per-run with --org.
org_prefixes = ["github.com/opentdf"]

# Check out only each module's own subdirectory
sparse = true

# Run drift detection right after creating a rig
verify_on_new = true

# Pin drifted third-party versions back to the baseline automatically
freeze = false

[ui]
# Display icons in output
icons = false

# Display home directory as ~ in output
tilde_home = true
```

Edit this file to customize discovery behavior. The optional `[dash]`
section configures [`wgo dash`](#dashboard-configuration).

The `[worktree]` and `[rig]` paths encode wgo's layout contract:

| Path | Holds |
|------|-------|
| `<mains_dir>/<owner>/<repo>` | The clone, checked out on trunk |
| `<worktrees_dir>/<slug>/<repo>` | One workspace per branch or issue |
| `<worktrees_dir>/pr-<N>-<slug>/<owner>/<repo>` | One workspace per PR |
| `<rig.dir>/<name>/src/<checkout>` | One pinned checkout per module of a [rig](#debugging-a-shipped-artifact-with-wgo-rig) |

## File Structure

wgo maintains the following files:

- **`~/.plan`** — Symlink to your plan file (for easy access)
- **`~/.wgo/plan.md`** — Your actual plan file (human-editable markdown)
- **`~/.wgo/state.json`** — Persistent state (repos, annotations, metadata)
- **`~/.wgo/config.toml`** — Configuration settings

## Workflows

### Daily Planning and Status

Start your morning with a clear picture of what you shipped yesterday, what's waiting on review, and where you left off.

```bash
# Full morning review: yesterday's commits, PR activity, active branches, open tasks
wgo today

# Explicitly review the previous day
wgo today --yesterday
```

**Sample output:**
```
=== Today: Wednesday, March 11 ===

Commits (3):
  abc1234  virtru/platform  fix: resolve auth token refresh on retry
  def5678  virtru/wgo       feat: add status dashboard watch mode
  789abcd  virtru/api       chore: bump dependency versions

PRs Needing Attention (2):
  #142  virtru/platform  fix/auth-refresh         review requested
  #98   virtru/wgo       feat/status-dashboard    CI failing

Active Branches (5):
  virtru/platform  fix/auth-refresh        2 modified
  virtru/wgo       feat/status-dashboard   clean
  ...
```

Check your contribution heatmap for a weekly cadence review:

```bash
wgo contrib           # activity across all repos
wgo contrib --weeks 4 # zoom out to the past month
```

See what PRs need your attention, then review your task list:

```bash
wgo pr        # PRs needing attention (review requests, failing CI, stale)
wgo plan      # current plan and task list
```

Track tasks through the day:

```bash
wgo add "write tests for the auth refresh fix"
wgo add "respond to PR #142 review comments"

# ... work happens ...

wgo done "write tests"     # mark matching task complete
wgo done "PR #142"         # fuzzy-matches against task text
```

Pulse check when switching into a repo:

```bash
cd ~/code/virtru/platform
wgo .
# repo:   platform
# branch: fix/auth-refresh
# status: 2 modified
# remote: ↑1 ↓0 (origin/fix/auth-refresh)
# commit: abc1234 fix: resolve auth token refresh on retry (3 hours ago)
```

Find repos with uncommitted work before you wrap up:

```bash
wgo status --since today --filter dirty
```

---

### Getting Stuff Done

Full feature lifecycle — from getting assigned an issue through merge.

Start by finding what's in your queue:

```bash
wgo pr           # PRs needing attention across all repos
wgo pr --mine    # just your open PRs and their CI status
```

Jump to an issue to start work — `wto` handles cloning, branch creation, and worktree setup in one command:

```bash
wto https://github.com/virtru/platform/issues/42
# clones repo if needed, creates branch "42-fix-auth-token", opens worktree
# you're now in the worktree
```

Immediately annotate the branch with its purpose:

```bash
wgo plan add "Fixing auth token refresh on retry — issue #42"
```

Break down the work into tasks:

```bash
wgo add "reproduce the token refresh failure"
wgo add "write a regression test"
wgo add "fix the retry logic in auth.go"
wgo add "update CHANGELOG"
```

While working, keep an eye on status and close tasks as you go:

```bash
wgo .                          # in-context pulse check
wgo status --watch             # monitor multiple repos while context-switching
wgo done "reproduce"           # close completed tasks
wgo done "regression test"
```

After pushing, jump to your PR:

```bash
wto https://github.com/virtru/platform/pull/123
wgo pr --mine    # confirm CI is green, review status
```

After the PR merges, clean up:

```bash
wgo clean --branches    # remove local merged branches
```

---

### Code Review Workflows

Efficiently review PRs — get the code locally fast, leave useful feedback.

See what's assigned to you for review:

```bash
wgo pr --review
# #156  virtru/platform  feat/new-billing-flow     review requested (2 days ago)
# #161  virtru/api       fix/rate-limit-headers    review requested (4 hours ago)
```

One command to get the code locally in an isolated worktree — no manual `gh pr checkout`, no branch collision with your own work in progress:

```bash
wto https://github.com/virtru/platform/pull/156
# fetches PR branch into a new worktree
# you're now in ~/code/virtru/platform-pr-156
```

Confirm you're on the right branch and commit:

```bash
wgo .
# repo:   platform
# branch: feat/new-billing-flow
# commit: e3f9a12 feat: add Stripe billing integration (1 day ago)
```

Use Claude Code to accelerate the review:

```bash
claude   # launches Claude Code in the worktree with full repo context
# ask: "Review this PR for security issues, edge cases, and API design"
```

After submitting your review, clean up the worktree:

```bash
wgo clean --worktrees    # removes worktrees for merged/closed PRs
```

---

### Keeping It Clean

Weekly hygiene — prune merged branches, stale worktrees, unused forks, and dead draft PRs.

Find repos that haven't had recent activity:

```bash
wgo status --filter stale          # repos with no commits recently
wgo contrib --weeks 4              # see which repos you haven't touched
```

Check for trunk checkouts that have drifted — stranded work, or a leftover
`<worktrees_dir>/<trunk>/<repo>` duplicate of the clone:

```bash
wgo doctor
#   ~/Documents/GitHub/mains/virtru/platform
#     work stranded on @ (2 change(s)); run `wgo park ~/Documents/GitHub/mains/virtru/platform`
#     to move it to ~/Documents/GitHub/worktrees/fix-auth/platform
```

`wgo doctor` only reports. `wgo park` is what moves the work.

Always preview before removing anything:

```bash
wgo clean --dry-run
# Would remove worktree: ~/code/virtru/platform-pr-156 (PR #156 merged)
# Would remove branch:   fix/old-auth (merged to main 3 weeks ago)
# Would remove worktree: ~/code/virtru/api-pr-98 (PR #98 closed)
# 3 items total. Run without --dry-run to remove.
```

Then clean up interactively (the `[y/N/a/q]` prompt lets you confirm each item, accept all, or quit):

```bash
wgo clean --worktrees    # remove worktrees for merged/closed PRs
wgo clean --branches     # remove local branches for merged PRs
wgo clean --repos        # remove unused fork checkouts
wgo clean --remote       # close draft/stale PRs on GitHub (with confirmation)
```

`--worktrees` (and plain `wgo clean`) also forgets jj workspaces whose directory
was deleted — the "listed by jj but its directory is gone" warnings in `wgo dash`.
That only runs `jj workspace forget`: nothing on disk is removed, and edits in the
workspace's working-copy change stay in the repo.

For scripting or CI, skip prompts with `--yes`:

```bash
wgo clean --worktrees --branches --yes
```

After cleanup, confirm signal-to-noise improved:

```bash
wgo status --sort activity    # active repos should now be at the top
```

---

## Stacked Pull Requests

When a change is too big for one PR, split it into a *stack* of small, dependent PRs and let `wgo` keep them in sync. Each PR in the stack targets the previous one as its base (instead of `main`), so reviewers can read one focused change at a time and you can land the bottom of the stack while the top is still in review.

`wgo` tracks the stack as a DAG in `~/.wgo/state.json`, keyed by the repo's canonical main checkout path rather than the current worktree path. That keeps stack membership stable even if a branch is opened from different worktrees or a worktree is later moved/recreated. The topology is also mirrored into each PR body as a fenced `<!-- wgo-stack -->` block so reviewers can see it and another machine can rebuild local state from GitHub.

### Quick start (new stack)

```bash
# 1. Start a stack from the current branch (becomes the root)
wgo stack new big-feature

# 2. Layer the next change on top — creates a worktree on the parent's tip,
#    pushes, and opens a draft PR with the correct --base
wgo stack push feat/02-plumbing --on big-feature --draft

# 3. After editing the bottom layer, rebase everything downstream
wgo stack restack big-feature
# - rebases each child in its own worktree (topological order)
# - one atomic `git push --atomic --force-with-lease=...` per repo
# - refreshes the marker block in every affected PR body
```

### Adopting an existing chain

If you already built a stack with plain `git` + `gh`, hand it over to `wgo`:

```bash
wgo stack adopt big-feature \
  feat/01-refactor \
  feat/02-plumbing \
  feat/03-ui
# adopted 3 branch(es) into stack "big-feature"
#   feat/01-refactor ← root
#   feat/02-plumbing ↳ on feat/01-refactor
#   feat/03-ui       ↳ on feat/02-plumbing
```

Branches are linked in the order given (linear adoption only — DAG with merge nodes is supported by `restack` but adoption from PR-graph heuristics is future work).

### Keeping the stack in sync

Once a parent PR merges, run `wgo stack sync` (no rebasing — just GitHub housekeeping):

```bash
wgo stack sync
# - retargets each child whose parent has merged: `gh pr edit --base <new-base>`
# - removes the merged parent from the child's recorded Parents
# - refreshes the marker block in every PR body so reviewers see the new topology
```

After conflicts during `wgo stack restack`, the affected worktree is left in the rebase/merge state and a checkpoint is written to `~/.wgo/cache/restack-<id>.json`. If a downstream branch has no worktree checked out, `wgo` recreates it under `worktree.worktrees_dir` before rebasing. Resolve conflicts by hand, then:

```bash
wgo stack restack --continue
```

### Configuration

Stack commands need nothing beyond what `wgo` already requires:
- `worktree.worktrees_dir` in `~/.wgo/config.toml` — where new worktrees from `wgo stack push` land. See [Configuration](#configuration).
- `gh` on `PATH` and authenticated, for `--draft` PR creation, `stack sync`, and marker-block updates. Without `gh`, the local rebase + force-with-lease path still works.

### Current view

```bash
wgo stack status                    # the DAG for the current branch's stack
wgo .                                # adds a "stack: a → **b** → c" line when applicable
wgo stack rm <branch>                # refuses if it has unmerged children
```

---

## Debugging a Shipped Artifact with `wgo rig`

A **rig** is a directory of jj workspaces, each pinned to the commit a released
artifact shipped with, wired together by a generated `go.work`.

It answers one question: *what was actually running in production?* You attach a
debugger to the exact code that shipped, across every repo it came from, and step
through it. Without a rig you are checking out nine repos by hand, guessing at
which tag each one was on, and discovering three hours later that one of them was
a pseudo-version.

Rigs are deliberately **disposable and separate from your branch work**. They live
under `rig.dir`, not `worktrees_dir`; `wgo ls`, `wgo status`, `wgo clean` and
`wgo doctor`'s spec checks all skip them. Nothing in a rig is meant to be pushed.

### Quick start

```bash
# From the shipped binary — best fidelity: build info records the versions that
# were actually linked, rather than re-deriving them and risking a different
# resolution.
cd $(wgo rig new dsp-2.7.1 --from-binary ./dsp)

# Or from the artifact's tagged source, whose dependency set is read with `go list`
cd $(wgo rig new dsp-2.7.1 --from virtru-corp/data-security-platform@v2.7.1)
```

`wgo rig new` prints **only the rig path** to stdout, so it composes with `cd`.
Everything else goes to stderr.

Before running any `go` command in a rig, point `GOWORK` at the rig's `go.work` —
the checkouts ship their own, and Go's upward search finds those first:

```bash
source env.sh                            # written into the rig
eval "$(wgo rig show dsp-2.7.1 --env)"   # or, from anywhere
```

### What you get

```
~/Documents/GitHub/rigs/dsp-2.7.1/
├── rig.toml     # the manifest: pins, members, sparse sets, baseline versions
├── go.work      # binds every checkout together
├── env.sh       # exports GOWORK
└── src/
    ├── data-security-platform-v2.7.1/
    ├── platform-service-v0.11.6/      # sparse: only ./service is materialised
    └── otdfctl-v0.26.2/
```

Checkouts are **sparse by default** — a monorepo checked out in full, once per
pinned module, costs gigabytes and as many editor indexes. They carry **no
bookmark**: they are pinned to tags, so there is nothing to push from them. Land
fixes from a normal worktree instead.

`rig.toml` is hand-editable. Abbreviated commit hashes pasted in from `jj log`
work fine.

### Checking a rig still reproduces the artifact

A rig can be wrong in a way that nothing complains about: `go.work` still
resolves, `go build` still succeeds, and the source under your debugger is
quietly not what shipped. Two commands cover the two ways that happens.

```bash
# Third-party drift: does MVS across the rig still resolve every dependency to
# the version the artifact recorded?
wgo rig verify dsp-2.7.1
wgo rig verify --all                    # the whole module graph, not just the
                                        # modules contributing packages
wgo rig verify --freeze                 # pin drifted versions back to baseline
wgo rig verify --unfreeze golang.org/x/net
wgo rig verify --format json            # for CI
```

```bash
# Checkout drift: has someone run `jj new`/`jj edit`/`jj rebase` in a checkout,
# moving it off its pin? doctor reports it and names the fix; it never moves
# anything itself, because restoring a pin discards whatever you moved it to.
wgo doctor

#   ~/Documents/GitHub/rigs/dsp-2.7.1 [platform-service-v0.11.6]
#     moved off its pin: opentdf/platform is pinned to service/v0.11.6 but sits on cccccccc5555.
#     put it back with `jj -R .../src/platform-service-v0.11.6 edit 6a5827d70000`,
#     or re-materialise it with `wgo rig sync dsp-2.7.1`
```

`wgo .` and `wgo statusline` both lead with a `⚓ <rig>@<pin>` segment inside a
rig, because every other field reads misleadingly there — the checkout has no
bookmark, nothing to be ahead or behind of, and no PR to find.

### Keeping a rig current

```bash
wgo rig sync dsp-2.7.1              # re-resolve pins and bring checkouts back in line
wgo rig sync --prune                # also drop checkouts no member needs
wgo rig sync --dry-run              # preview

wgo rig add dsp-2.7.1 -m github.com/opentdf/platform/sdk@v0.4.2   # by hand
```

### Managing rigs

```bash
wgo rig ls                          # table on a TTY, one path per line when piped
wgo rig show dsp-2.7.1              # modules, pins, sparse sets
wgo rig rm dsp-2.7.1                # forget the jj workspaces, then delete the tree
```

Use `wgo rig rm` rather than `rm -rf`: the rig registered a jj workspace in each
module's main clone, and deleting the directory by hand leaves those workspaces
registered with nothing on disk naming them.

`rig rm` also clears a half-built rig — the directory an interrupted `rig new`
left behind, with checkouts but no `rig.toml`. It traces each one back to its
main clone and lists what it found; because there is no manifest to tell a clean
checkout from a dirty one, removing it takes `--force`:

```bash
wgo rig rm repro-2.7.1-hsm --force
```

Interrupting `rig new` should not get you there in the first place: Ctrl-C rolls
the partial rig back, and a second Ctrl-C abandons the rollback (exit 130) and
leaves the rig in place for `rig rm --force`.

---

## Year in Review (Claude Code skill)

This repo ships a `/year-in-review` Claude Code skill at
`.claude/skills/year-in-review/`, with its agents in `.claude/agents/yir-*.md`.
It turns your GitHub and Jira activity for a period into an evidence-backed
report grouped by theme, effectiveness, collaboration, and complexity. It
isn't a `wgo` command, but it uses `wgo ls` to find local repos that have no
GitHub activity, and saves a `wgo contrib` heatmap alongside the run.

```
/year-in-review                  # year to date
/year-in-review 2026-H1 collab   # one aspect
/year-in-review 2026 focus:zip64 # deep dive on one theme
```

It needs `gh` (with the `read:org` scope for the team map) and, optionally,
`acli` for Jira. `collect.py` caches closed months under
`~/.wgo/cache/review`. The first full-year run takes a few minutes because
GitHub search is limited to 30 requests a minute. Reports are written to
`~/.wgo/reviews/<label>.md`, named after the run: `2026-Q3.md`, or
`2026-to-2026-10-09.md` for a period still in progress. The skill is read-only
against GitHub and Jira.

### Exploring a year in review

`wgo review graph` turns a finished run (under `~/.wgo/cache/review/runs/`)
into an influence graph: people, PRs, tickets and repos, joined by who wrote,
reviewed, built on or referenced what. Pass a run label or directory, or omit
it for the most recent run.

```
wgo review graph | jq '.nodes | length'          # JSON on stdout
wgo review graph --open         # offline explorer, opened in the browser
wgo review graph 2026-H1 --out /tmp/h1.html      # .html extension selects html
wgo review graph --format html --out - > e.html  # html to stdout
```

The HTML is a single self-contained file with no network requests, so it can
be shared as-is. Without `--out` it is written to
`~/.wgo/reviews/<label>.graph.html`.

## Live Dashboard with `wgo dash`

`wgo dash` shows all your ongoing work in one live page: efforts,
workspaces, bookmarks and their PRs, tickets and agent sessions, with how
fresh each source is and what changed since you last looked. It also shows
every PR you and the [people you follow](#dashboard-configuration) author
or are asked to review, even with no local bookmark (see
[PR inventory](#pr-inventory)).

```
wgo dash                 # serve http://127.0.0.1:8766 and open it
wgo dash --no-open       # serve without opening a browser
wgo dash --port 8800     # another loopback port
wgo dash --json          # print one snapshot as JSON and exit
```

The server listens on the loopback address only. Every 30 seconds by
default (`[dash] refresh_seconds`) it re-collects local jj state, reading
with `--ignore-working-copy` so it never snapshots a workspace. It also
refreshes stale PR, PR inventory, Jira and GitHub issue data. Year-in-review runs are
served under `/review/`. Stop the server with Ctrl-C.

With `[jira] board` set, the dashboard also shows that board's active and
future sprints, with their goals and dates, and every ticket in an active
sprint assigned to the people it follows, including tickets no PR or
workspace leads to; those are grouped under their sprint. See
[Jira sprints and ticket facts](#jira-sprints-and-ticket-facts).

To open a workspace from a `wgo://open?ws=<id>` link outside the dashboard,
install the optional [macOS URL handler](#wgo-links-with-wgo-setup-url-handler).
The dashboard does not need it.

### Queries and Markdown extracts

One query language filters the page, the CLI and saved filters. A query is
clauses joined by `;`, all of which must hold. Each clause is
`[!]field:value`, where the value is one of:

- a value, or a comma-separated list (any of them): `prstate:open,draft`
- `*`, any known value: `repo:*`
- a range: `activity:2026-10-01..2026-10-07`, `activity:-7d..`, `>=30`

Fields cover local work (`kind`, `text`, `effort`, `repo`, `activity`,
`when`, …), PRs (`author`, `requested_from`, `reviewer`, `merged`, `age`,
`mergeable`, `check_failing`, …), Jira tickets (`status_category`,
`sprint`, `sprint_state`, `points`, `epic`, …) and the
[attention facts](#attention-facts) (`move`, `move_people`, `waiting_on`,
`reason`, `waiting`, `ci`, `size`, `size_bucket`, `stale`, `mismatch`,
`linked_prs`); a misspelt field lists them all. `@me` and `@people` stand for the configured people. A clause never
matches a node whose value is unknown, negated or not; add `unknown` to a
list to include those. `kind:` clauses choose which matches are rows.

The page keeps the query, view, sort, columns and selection in its address,
so every state is a link and Back undoes the last change. Chart bars add or
remove a clause. **Copy as Markdown** copies the current rows.

The **Table** view compares the rows. Its columns come from the query
fields, chosen and ordered with **Columns…**; the defaults suit the rows:
PRs get repository, author, whose move, waiting, age, size, CI, reviews and
tickets; tickets get status, assignee, points, sprint, linked PRs and
mismatches. Click a header to sort, again to reverse, and Shift-click to add
a second or third key; unknown values always sort last. Durations and sizes
carry a small bar beside the number (sizes on a log scale), CI one mark per
check with failures first, and reviews one glyph per reviewer (`✓` approved,
`✕` changes requested, `○` requested, `…` commented). Without a `kind:`
clause, nodes kept only because a match leads to them are listed dimmed and
can be hidden. Arrow keys move between rows, Enter selects and Shift+Enter
adds. Copy as Markdown lists the table's rows in its order, as one list,
with its columns; the table's own State, Data and Last activity columns
have no Markdown form, and the status line says when a sort on one of them
was left out.

```
wgo dash --query 'kind:pr;prstate:open'           # open the page at this query
wgo dash --json --query 'repo:acme/api;kind:pr'   # adds "query": {expr, matches, rows}
wgo dash --md --query 'kind:pr' --cols prstate,checks --group repo
wgo dash --md --preset shipped --layout table     # a saved filter as a Markdown table
wgo dash --md --query 'kind:pr' --sort -updated_ago,repo --group none   # one list, as the table sorts
```

A malformed query fails with the column, the fix and a corrected command.
`GET /api/extract?q=…&cols=…&sort=…&group=…&layout=…` serves the same
Markdown as `--md`, byte for byte.

Saved filters are built in or set under `[dash.filters]`, where they
override built-ins of the same name. `--preset NAME` runs one, and the
page's **Saved filters** chips list them. The built-ins:

| Name | Query | Answers |
|------|-------|---------|
| `my-move` | `move_people:@me` | my PRs to act on, and reviews asked of me, through a team too |
| `waiting-reviewers` | `kind:pr;move:reviewer` | PRs waiting on a reviewer |
| `push-reviewers` | `kind:pr;move:reviewer,nobody;ci:green,systemic_only` | PRs worth a nudge: reviewers' or nobody's move, and CI not at fault |
| `nobody` | `kind:pr;move:nobody` | ready PRs with no reviewer and no approval |
| `review-inbox` | `kind:pr;requested_from:@me` | reviews asked of me |
| `shipped` | `kind:pr;prstate:merged;merged:-7d..` | what merged in the last week |
| `close-candidates` | `kind:pr;prstate:open,draft;updated_ago:>=90` | open PRs untouched for 90 days |
| `jira-out-of-sync` | `kind:ticket;mismatch:*` | tickets whose Jira status disagrees with their PRs |
| `sprint` | `kind:ticket;sprint_state:active` | the active sprint's tickets |

A filter that does not parse, or needs a field not registered yet, is
hidden; `--json --query` lists each filter with the reason it is hidden.

```toml
[dash.filters]
api-prs = "kind:pr;repo:acme/api;!prstate:merged,closed"
```

### Linked filters

The **Filters** panel left of the views lists facets: whose move, CI, PR
state, repository, author, reviewer, waiting, age, size, ticket status,
sprint and Jira mismatch. Each is a small chart that is also a filter. Its
light grey bars count the whole snapshot; its dark bars count what the
*other* filters leave, so a near miss shows before you widen a filter. A
selection shows as a thin blue bar inside every facet. Unknown values have
a row of their own. Long lists show the top 8 and a box to find the rest;
author and reviewer list the people you follow first. Facets with no
known value are hidden, and **More filters…** adds any other field.

| Pointer | Keyboard | Effect |
|---|---|---|
| click | Enter or Space | only this value (`repo:acme/api`); again clears |
| Shift- or ⌘-click | Shift+Enter | add or remove the value (`repo:acme/api,acme/web`) |
| Alt-click | Alt+Enter | exclude it (`!repo:acme/web`) |
| drag a histogram's handles, or across days | arrows, Shift+arrows | set a range (`size:30..299`, `when:2026-10-16..2026-10-22`) |

A chosen value is outlined and marked `✓`, an excluded one struck through
and marked `✕`. Each facet shows its clauses and a **Clear** button, and
**Clear all** empties the query. A drag updates the page as you go and
is one step for Back.

The overview starts with **Attention**: how many PRs are your move, wait
on reviewers, or are nobody's move. Each count links to its saved filter
and equals that filter's row count. Beside it, the **When** chart counts
items per day. Drag across it to set `when:a..b`; **Clear** removes it.
Days before the local activity window (`[dash] days`) are hatched, since
local work there is not recorded, and each other day's tooltip gives its
local change count. Under `shipped` the chart counts merges per day, and a
drag replaces `merged:-7d..`, so any week answers "what shipped then". With a time range set, Attention and **PRs by state**
compare it with the period of equal length before it ("3 merged · previous
0"). When the PR inventory does not cover that period, they say so instead
of showing a partial count. The agent sessions chart shows only when there
are sessions. **Copy as config** copies a `[dash.filters]` line for the
current query; the page never writes your config.

`[dash] facets` sets the panel's facets and their order:

```toml
[dash]
facets = ["move", "repo", "reviewer", "size", "mismatch"]
```

A name that is not a field is reported on stderr and in the panel, and
left out.

### Scatterplot and CI grid

The **Scatter** view asks which PRs are small and have waited long (push
them) and which are big (split them). Each PR row of the query is a mark:
waiting time across, size up, by default; either axis can be any PR number
or duration field, kept in the address as `x=` and `y=`. Sizes and durations
use log scales, labelled so, with gridlines at 1, 10, 100, 1,000 lines and
1, 7, 30, 90 days. A mark's shape says whose move it is: ● a reviewer's, ▲
the author's, ■ nobody's, ○ unknown, ◆ none (merged or closed). It is blue,
with a dot in it, only when the move is yours, and carries a small × when
the PR's own checks fail. A PR whose value on an axis is unknown, or that
waits on no one (the author's move, or closed), sits in a labelled strip
along that axis, never at zero. Up to five outliers (largest on both axes
together) and the selection are labelled; hover any other mark for its
details. **Median lines** (`median=on`) add the median of each axis.

Drag a rectangle to select (Shift adds), click a mark, and scroll or pinch to
zoom (**Reset zoom**, or `0`). The selection is the page's: the table, graph
and detail panel follow it. By keyboard, Tab into the chart: the arrow keys
walk the PRs in the table's sort order, Enter selects, Shift+Enter adds, and
`+` and `-` zoom. With 1,000 PRs or more, the marks are drawn on a canvas.

The **CI grid** asks whether failures are a PR's own or the check fails
everywhere. Each repository gets a table: a column per PR (most failing
checks first, then by number; past 60, a "+n more" button narrows the query
to that repository) and a row per check that ran on any of them. A cell is
`×` failing, `✓` passing, `·` pending, blank for skipped or neutral, empty
when the check did not run, and `?` when it is unknown because the PR lists
more than 100 checks (its column is marked `+`). A check that
[fails everywhere](#attention-facts) is labelled "fails everywhere — 5 of
6 PRs" and drawn as a neutral solid line, so a PR's own failures stand out
in red. Click a check to filter with `check_failing:<name>` (again to
remove it), a PR to select it, or a cell to select its PR and show that
check's state and link in the detail panel (the address keeps it as
`check=<PR>|<check>`; selecting anything else forgets it). The arrow keys
move through a grid; Enter acts as a click.

### Sprint view

The **Sprint** view answers "does Jira agree with the code for this sprint,
and how far along is it against its goal?" It needs `[jira] board` (see
[Jira sprints and ticket facts](#jira-sprints-and-ticket-facts)); without
it, or while the board's sprints are unknown or failed to load, the view
says which and how to fix it.

- **Which sprint.** One active sprint at a time. With several, buttons
  choose one (`sprint=<id>` in the address); the default is the one with
  the most tickets assigned to the people the dashboard follows, matched
  by Jira account ID or found by the sprint's search for their tickets.
  When that cannot be told for some sprint (a `jira` of `currentUser()` or
  an email), the default is the sprint with the most tickets, and the
  button's tooltip says so.
- **Header.** Name, dates, the goal as written (or "no sprint goal"), and a
  thin bar of elapsed time with "day 6 of 21". Days are calendar days in
  the snapshot's timezone, counted from the start day, so a Jira sprint of
  whole weeks is 7, 14, 21 or 28 days long.
- **Points.** One bar of story points by status category — Done, In
  Progress, To Do, in that order so the done share lines up with the time
  bar above it — with each part labelled with its points and tickets.
  Unestimated tickets are counted apart ("1 unestimated"), and so are
  tickets whose points were never read ("1 with points unknown"); neither
  counts as zero.
- **Tickets.** Grouped by status category, then assignee: key and summary,
  assignee, [mismatches](#attention-facts) marked `!`, linked PRs (`◇`
  draft, `○` open and awaiting review, `✓` approved, `●` merged, `✕`
  closed; hover for whose move it is), points and status.
- **Filtering.** Clicking a points label or bar part applies
  `status_category:<c>`; clicking a mismatch applies `mismatch:<code>`.
  Tickets outside the query are dimmed, so the sprint stays whole;
  **Hide … outside the query** hides them instead (`outside=hide`). Rows
  select like the table's: arrow keys move, Enter selects, Shift+Enter adds.

**Copy as Markdown** on this view copies a sprint summary: dates, goal,
points by status (with the share the query shows), the mismatches on the
tickets the query shows with links to each ticket and PR, and the sprint
ID, query and source freshness. The CLI prints the same bytes:

```
wgo dash --md --view sprint                          # the default sprint
wgo dash --md --view sprint --sprint 1234 --query 'assignee:Alice'
```

`GET /api/extract?view=sprint&sprint=<id>&q=…` serves it too. A sprint ID
that is not an active sprint fails with the list of active ones.

### Actions

Select a workspace to get these buttons:

| Button | What it does |
|--------|--------------|
| **Open tab** | Opens a new terminal tab in the workspace. |
| **Resume** | Opens a tab and runs `claude --continue` or `codex resume --last` there. Shown only when `[dash] resume` is set. |
| **Editor** | Opens the workspace in your editor. |
| **Finder** | Reveals the workspace in Finder. |
| **Plan** | Opens `~/.wgo/plan.md` at this bookmark's Active Branches line. |
| **Spec** | Opens `spec/<ticket>.md` for the ticket in the bookmark name. |

**Mark seen** in the *Since last look* box makes the current snapshot the
baseline for counting changes.

Each button reports what happened under it:

- which launcher it used;
- which launchers it tried first, and why they failed;
- when nothing could be launched, a shell-quoted `cd '<path>'` (or a
  path) with a **Copy** button.

Errors appear in the same place, for example a workspace that no longer
exists, or a ticket with no spec, which names the command to write one
(`wgo spec new GH-70`).

A launch never changes jj, the plan or agent state.

**Launch order:**

- **Open tab:** Ghostty, then `terminal_command` if set, then the `cd`
  command to copy. Set `terminal = "iterm"` or `"terminal"` to use iTerm or
  Terminal.app instead.
- **Resume:** runs in a Ghostty tab. With any other terminal, wgo opens the
  tab and gives you the command to paste.
- **Editor, Plan and Spec:** `[dash] editor`, then `code`, then `xed`, then
  Finder, then the path to copy. Plan opens at its Active Branches line
  with `code -g file:line` or `xed -l line`. Spec opens the file.

**When the baseline moves.** Only Mark seen moves it. Loading the page,
reloading it or closing it never does. The baseline is shared by every tab,
so a page-close acknowledgement would quietly reset the counts that another
open tab is still showing.

### Security model

The action endpoints only accept requests from the page that this run of
`wgo dash` served:

- **Page token.** Each run of `wgo dash` makes a random 256-bit token. It is embedded
  only in the served page, never in a URL or a log, and pages carry
  `Referrer-Policy: no-referrer` and `Cache-Control: no-store`. The page
  sends it in an `X-Wgo-Token` header, which the server compares in
  constant time. Reload the page after restarting `wgo dash`.
- **Origin and Host.** `POST /api/action` and `POST /api/ack` also require:
  - an `Origin` exactly equal to `http://127.0.0.1:<port>`;
  - the exact `Host`, which defeats DNS rebinding;
  - `Content-Type: application/json`;
  - a body of at most 4 KiB;
  - no unknown JSON fields.
- **No cross-origin access.** There are no CORS headers, and a preflight
  gets 405.
- **IDs, not paths.** A request names only an action kind and a workspace
  ID. The server re-resolves the ID through discovery every time. It checks
  that the directory exists, contains `.jj`, and is inside a `[discovery]
  base_dirs` root, after resolving symlinks. It then derives every path itself. An
  unknown or stale ID gets a 404 and launches nothing.
- **No shell from a request.** wgo never starts a shell itself: launchers
  run as argv lists. Ghostty is driven by a fixed AppleScript that receives
  the path as an argument and never interpolates it. The one text that
  reaches a shell is Resume's command, typed into the new tab's shell, and
  it is only ever `claude --continue` or `codex resume --last` from your
  config, never anything from the request.

### macOS Automation prompt

The first Open tab or Resume with Ghostty runs `osascript`. macOS then asks
whether **"wgo" wants to control "Ghostty"**; the prompt may name your
terminal or `osascript` instead. Click **Allow**. If you click **Don't
Allow**, or the prompt times out, wgo falls back to the next launcher.
When macOS reports the denial, the fallback note says how to fix it.

To change your answer, open **System Settings > Privacy & Security >
Automation**, expand the app that ran `wgo dash`, and toggle **Ghostty**. To
be asked again, reset Apple Events consent for all apps:

```
tccutil reset AppleEvents
```

Ghostty needs version 1.3 or newer with `macos-applescript` enabled, which
is the default.

### Dashboard configuration

All `[dash]` keys in `~/.wgo/config.toml` are optional:

```toml
[dash]
port = 8766                # loopback port; --port wins
days = 14                  # activity window; --days wins
refresh_seconds = 30       # background re-collection interval (5 to 3600)
terminal = "ghostty"       # Open tab: ghostty | iterm | terminal | command
# argv for terminal = "command", and the fallback after Ghostty when set.
# An element that is exactly "{workspace}" becomes the workspace path; no shell.
terminal_command = ["wezterm", "start", "--cwd", "{workspace}"]
resume = "claude"          # Resume: "claude" or "codex"; unset hides Resume
editor = "zed"             # tried before code and xed; one executable, no arguments
```

An out-of-range `port`, `days` or `refresh_seconds` stops `wgo dash` with
an error naming the setting; these are never silently adjusted. `--json`
ignores `port` and `refresh_seconds`, since it prints one snapshot without
serving.

`[[dash.people]]` names the people whose PRs and tickets the dashboard
follows, one table per person:

```toml
[[dash.people]]
github = "alice"           # GitHub login (required)
jira = "alice@example.com" # Jira account ID, email or currentUser() (optional)
name = "Alice"             # display name (optional)
```

Without entries it follows you (the authenticated GitHub user) and
`[pair]`'s teammate; you are always included. An entry without a GitHub
login, with something else in its place (`@alice`, an email, a team), or
repeating another entry's login stops `wgo dash` with an error naming the
entry.

Two more `[dash]` keys tune the [PR inventory](#pr-inventory):

```toml
[dash]
inventory_ttl_seconds = 300   # open PR sets stay fresh this long (60 to 86400)
ticket_prefixes = ["TEAM"]    # Jira keys that count in PR titles and bodies
```

`inventory_ttl_seconds` sets how long the open sets stay fresh (300
seconds by default). The closed-recent set changes only when a PR merges
or closes, so it stays fresh six times as long (1800 seconds by default).
A value outside 60 to 86400 stops `wgo dash` with an error naming it.

`[dash.facts]` sets the thresholds of the [attention facts](#attention-facts):

```toml
[dash.facts]
stale_days = 30           # an open PR not updated this long is stale (1 to 3650)
systemic_ratio = 0.6      # a check failing on this share of a repository's open PRs fails everywhere (above 0, at most 1)
systemic_min_prs = 3      # ...if it ran on at least this many open PRs (2 to 1000)
```

A value out of range, `0` included, stops `wgo dash` with an error naming
the key, its range and its default.

`ticket_prefixes` lists the Jira project keys whose ticket IDs count when
they appear in a PR's title or body, so `SHA-256`, `AES-256`, `P-384` or
`UTF-8` are never read as tickets. Without it, the projects in `[jira]`
(`project`, `default_project` and each `project_rules` entry) count. The
prefixes of tickets in your local bookmark names always count too. A
ticket in the head branch name of a PR your bookmarks lead to counts
whatever its prefix, as it does for bookmarks; on other PRs it needs a
known prefix too, once any is known, so GitHub's `patch-1` and
`revert-45-…` branches are not tickets. A reference to an issue number
that turns out to be a PR is dropped. A prefix with anything but letters is
an error.

### Jira sprints and ticket facts

Ticket nodes carry the facts Jira reports through `acli`: summary, status
category, type, priority, sprints, story points, parent epic, team,
components, labels, resolution and dates. Story points, sprint and team are
custom fields whose IDs differ per site. Name yours in `[jira.fields]`:

```toml
[jira]
board = 42                          # the board whose sprints wgo dash shows

[jira.fields]
story_points = "customfield_10016"  # or "none" if the site has no such field
sprint       = "customfield_10020"
team         = "customfield_10001"
epic_link    = "customfield_10014"  # legacy epic link; not needed when epics show as parent
```

`acli` cannot list a site's fields, so only the sprint field is discovered
on its own, from a ticket that is in a sprint. The discovery is cached for
7 days in `~/.wgo/cache/jira/fields.json`. A field that is neither set nor
discovered stays unknown, and the dashboard says which key to set. It never
shows a blank as "none". Find a site's IDs in any ticket's JSON:
`acli jira workitem view TEAM-123 --fields '*all' --json`.

The board's sprints are cached for 15 minutes in
`~/.wgo/cache/jira/sprints/board-<id>.json`. Each active sprint's search
is bounded to 200 tickets assigned to the people `[[dash.people]]` names
(their `jira` values, plus `currentUser()`); a sprint with more is marked
truncated. A Jira identity that Jira rejects is reported with the config
key it came from, and the others' tickets are still shown. A sprint whose
search failed keeps its last known tickets, marked stale, and is retried
after 2 minutes. Without `acli`, or with its login expired, the `jira` and
`jira_sprints` sources are unknown or failed and say why, and the rest of
the dashboard is unaffected. `wgo statusline` still reads only the ticket's
cache entry.

A `terminal`, `terminal_command` or `resume` value of the right type that
wgo does not support (an unknown terminal, `terminal = "command"` with no
command, a resume tool not on the list) does not stop the dashboard. `wgo
dash` prints a warning on stderr and ignores it. `terminal` and
`terminal_command` are checked together and dropped together, which means
Ghostty on macOS. A value of the wrong type is a config error like any
other key.

`editor` is a single executable name or path; `~` is expanded. It cannot
carry arguments, so `"code --wait"` is not found. Use an editor command
that returns at once, since wgo waits for it (up to two minutes) before
reporting.

### PR inventory

Besides the PRs your bookmarks lead to, the dashboard keeps three sets of
PRs for each person it follows:

| Set | What it holds |
|-----|---------------|
| `open-authored` | every open PR the person authored, draft or not |
| `review-requested` | every open PR asking for the person's review, directly or through one of their teams |
| `closed-recent` | the person's PRs merged or closed in the last `max(2 × days, 28)` days |

PRs in archived repositories are left out. A PR in several sets, or also
found through a bookmark, is one node. Each PR carries its author, times
(created, ready for review, merged, closed), size, mergeability, branch
names, review requests with when they were made, the latest review per
reviewer, every check on its head commit (up to 100), its ticket keys,
and each followed person's part in it (`author`, `review_requested`,
`reviewed`). A fact GitHub did not report is left out, never shown as zero.

A PR no bookmark leads to joins the graph at the first of:

1. the bookmark of a workspace in the same repository named like the PR's
   head branch;
2. the ticket effort (`TEAM-123`) of one of its ticket keys;
3. the effort of a workspace whose bookmark names one of its ticket keys;
4. a group per repository, listed after ticket groups and before
   Ungrouped.

The sets are fetched in the background through GitHub's GraphQL search, 20
PRs per page, retrying a page that fails with a server error or a timeout
after 1, 2 and 4 seconds. A set that still fails keeps its last good data,
marked stale, with the error shown on the `github_inventory` source, and
is retried after 1, 2, 4… minutes (at most 30), or when GitHub's
`Retry-After` says. A PR that leaves the open set (it merged or closed)
stays, marked stale, until the closed-recent set is refetched, which then
happens at once. GitHub
search returns at most 1,000 results, so a set with more is marked
truncated, with a diagnostic. Each set is cached under
`~/.wgo/cache/inventory/<login>/<set>.json`, and your GitHub login (looked
up once a day) under `~/.wgo/cache/inventory/identity.json`. Without a
`GITHUB_TOKEN` or `gh`, nothing is fetched and `github_inventory` says so.
`wgo dash` checks this when it starts, so restart it after `gh auth login`.

"Since last look" counts a PR only the inventory found as new when, at the
last look, one of the sets it is in was already known; otherwise it is
unknown, never silently new.

To compare your open PR count with GitHub's (which also counts archived
repositories):

```sh
wgo dash --json --refresh | jq '(.snapshot.people[] | select(.me) | .login) as $me
  | [.snapshot.nodes[] | select(.kind == "pr" and .pr.state == "open" and .pr.author == $me)] | length'
gh search prs --author @me --state open --limit 1000 --json number | jq length
```

### Attention facts

At the end of each collection, wgo derives the facts you would otherwise
work out by hand, each with a one-line evidence string built from its
inputs. There is no score: you rank by the facts you can see. The detail
panel shows them for a selected PR or ticket, and the graph's tooltip sums
them up.

**Whose move** a PR is (`move`) comes from the first of these rules that
holds; `reason` lists every one that holds, in this order:

| Rule | move | reason |
|------|------|--------|
| merged or closed | `none` | `closed` |
| draft | `author` | `draft` |
| a reviewer's latest review requests changes, and they were not asked again since | `author` | `changes_requested` |
| merge conflicts | `author` | `conflicts` |
| a check fails that does not fail everywhere (below) | `author` | `ci_failing` |
| approved | `author` | `approved_unmerged` |
| a review request is outstanding | `reviewer` | `awaiting_review`, and `team_only` when only teams are asked |
| none of these | `nobody` | `no_reviewer` |

`ci_systemic_only`, `ci_pending`, `no_ticket` and `stale` are listed too,
but never decide the move. `move_people` holds the move: the author, or the
reviewers asked, plus each person you follow who is asked through a team,
so `move_people:@me` is your move. `waiting` counts the days since the wait
began: since the earliest outstanding request (a later request never resets
it), since the PR was ready when nobody is asked, or since the review
requesting changes. A request of unknown time may have started the wait
earlier, and the page then says "or earlier"; with no request time known,
the wait is unknown.

**Fails everywhere.** For each repository, a check that ran on at least
`systemic_min_prs` open PRs and failed on at least `systemic_ratio` of
them fails everywhere (`check_stats` in the JSON). With the defaults, a
check failing on 2 of 3 open PRs does.
The count covers only the PRs the dashboard sees, those of the people you
follow, and the evidence says so. A PR whose only failures are such checks
has `ci: systemic_only`, and CI does not make it the author's move. A PR
with more than 100 checks lists only some, so a check missing from it is
counted as unknown there, not as not run.

**Jira vs code** (`mismatch`), for Jira tickets and their linked PRs
(`linked_prs`):

| Mismatch | Holds when |
|----------|------------|
| `code_merged_ticket_open` | every linked PR is merged or closed, at least one merged, and the ticket is not done |
| `ticket_done_code_open` | the ticket is done and a linked PR is open |
| `in_progress_no_code` | in progress, in an active sprint, with no linked PR or workspace |
| `in_progress_idle` | in progress, with linked PRs or workspaces but no activity on them for `stale_days` |
| `unassigned_in_sprint` | in an active sprint with no assignee |

A fact whose inputs are unknown (GitHub has not computed mergeability yet,
a PR known only through a bookmark, a ticket cached before its status
category) is `unknown` and names the missing input under `uncertain`. It is
never shown as false or zero.

## wgo:// links with `wgo setup url-handler`

On macOS you can let `wgo://open?ws=<id>` links open a terminal tab in a
workspace, for example from notes or a chat message. This is optional,
installed only when you ask for it, and independent of `wgo dash`.

```
wgo setup url-handler --dry-run     # print the steps, run nothing
wgo setup url-handler               # install and register the handler
wgo setup url-handler --uninstall   # unregister and remove it
wgo open 'wgo://open?ws=ws-0123456789abcdef'   # what a click runs
```

Setup builds a small AppleScript applet, `~/Applications/wgo URL
Handler.app`, with `osacompile`. It adds the `wgo` URL scheme and a wgo
marker to the applet's `Info.plist` with `plutil`, re-signs it ad hoc with
`codesign`, and registers it with LaunchServices (`lsregister -f`). The
applet passes the whole URL to the `wgo` binary that ran setup. It runs it
through AppleScript's `do shell script`, with the URL quoted by `quoted
form of`, so it arrives as one argument:

```
<absolute path of wgo> open '<url>'
```

Setup records that wgo path and your current `PATH` in the applet; wgo
needs `jj` on that `PATH`. Run setup again after moving wgo or jj. A
`go run` build is refused, because it is deleted when it exits. On other
systems setup reports that it is unavailable.

Workspace IDs are the `ws-…` IDs in the dashboard. List them with:

```
wgo dash --json | jq -r '.snapshot.nodes[] | select(.kind == "workspace") | "\(.id)  \(.workspace.path)"'
```

### What a link can do

Any web page can send you to a `wgo://` link, so `wgo open` treats the URL
as hostile:

- **One form only.** It accepts exactly `wgo://open?ws=<id>`, where the ID
  is `ws-` and 16 lowercase hex digits. The input must match the canonical
  form byte for byte. It rejects:
  - other hosts, paths, ports, user info and fragments;
  - extra, empty or repeated query parameters, and a trailing `&` or `;`;
  - percent-encoding, `+`, spaces, control characters and over-long URLs;
  - any `command`, `resume` or other parameter.
- **Known workspaces only.** The ID is resolved through current discovery.
  The directory must be an existing jj workspace under a `[discovery]
  base_dirs` root after resolving symlinks. This is the same check the
  dashboard's buttons use. An unknown, deleted or moved workspace is an
  error.
- **Open tab only.** A link opens a terminal tab with the dashboard's
  launcher and `[dash]` terminal settings. When no terminal can be opened,
  it prints a `cd` command to copy. It never resumes an agent and never
  runs anything from the URL.
- **First use asks.** The first link to a workspace shows a dialog with the
  workspace's full path. **Cancel** is the default, and the dialog cancels
  itself after two minutes. Click **Open** only if you just clicked that
  link. If you click **Open**, wgo remembers that per workspace ID and path
  in `~/.wgo/url-handler-approvals.json`, and later links to that workspace
  open without asking. If the ID later resolves to another path, wgo asks
  again. If the dialog cannot be shown (another OS, or no GUI session),
  `wgo open` asks y/N when you run it in a terminal, and otherwise refuses.
  It never accepts on its own.
- **Declining pauses that workspace.** **Cancel**, Escape, a timed-out
  dialog or a y/N "no" is not remembered as a refusal, but for the next 5
  minutes links to that workspace are refused with a notification instead
  of a dialog. A page that fires the same link again and again cannot wear
  you down into clicking **Open**. Other workspaces are not affected, and
  running `wgo open` yourself in a terminal skips the pause. Open it from
  `wgo dash` meanwhile.
- **No hidden formatting characters.** A workspace path containing control
  characters or invisible Unicode formatting characters (category Cf) is
  refused before any dialog.
  This covers bidi overrides and isolates (U+202A–U+202E, U+2066–U+2069),
  direction marks (U+200E, U+200F, U+061C) and zero-width characters, any
  of which could make the path in the dialog read as another. Look-alike
  letters and unusual spaces are not detected, so read the path.
- **One link at a time.** `wgo open` takes a non-blocking lock,
  `~/.wgo/url-handler.lock`. While one `wgo open` is handling a link, for
  example while its dialog is up, another `wgo open` exits at once with
  "another wgo:// link is already being handled", so a page cannot stack up
  dialogs. Separately, macOS itself may not deliver links that arrive
  while the applet is busy: in one test, of three links sent within a
  second, only the first ran and the others were dropped without a dialog
  or notification. That is macOS behavior, not something wgo enforces.

When `wgo open` fails, the applet shows why in a macOS notification, never
a modal alert. A web page can trigger this without any click from you.
Clicking **Cancel** is not a failure and shows nothing; a link refused
during the pause after it does show a notification. The messages never
include any part of the URL, so a page cannot put its own words in a wgo
notification.

When a link opens, the applet discards everything `wgo open` printed,
including warnings (say, approvals that could not be saved, or a `[dash]`
setting that was ignored). So `wgo open` also appends what each link did,
with a timestamp, to `~/.wgo/url-handler.log`. It records the workspace ID
and what happened, including local paths in error and fallback messages,
but never any part of the URL. Past 256 KiB the log starts over, keeping the previous one
as `url-handler.log.1`. To see why links misbehave:

```
tail ~/.wgo/url-handler.log
```

### Known limitation

Workspace IDs are unsalted hashes of the workspace and main clone paths.
A page that targets you and guesses your directory layout could compute a
real ID:

- For a workspace you have not approved, that only makes the first-use
  dialog appear.
- For one you have approved, the link opens a terminal tab in it with no
  prompt. It still cannot run anything in that tab.

A per-install salt would make IDs unguessable. That is a possible
follow-up. It would change the dashboard's workspace IDs.

### Automation prompt for links

The applet runs `wgo`, which drives Ghostty through `osascript`. The first
time a link opens a tab, macOS may ask whether **"wgo URL Handler"** may
control **"Ghostty"**. This is the same Automation (TCC) prompt described in
[macOS Automation prompt](#macos-automation-prompt), but it is a separate
grant for the applet. Change it under **System Settings > Privacy &
Security > Automation > wgo URL Handler**.

### Removing the handler

```
wgo setup url-handler --uninstall
```

This unregisters the applet from LaunchServices and deletes it. It first
checks that the applet's `Info.plist` carries the marker and bundle ID that
setup wrote. It refuses to touch anything else at that path, including a
symlink. Your approvals, the log and the lock stay behind in `~/.wgo/`
(`url-handler-approvals.json`, `url-handler.log*`, `url-handler.lock`);
delete them to forget your answers.

---

## Commands Reference

| Command | Description |
|---------|-------------|
| `wgo .` | Show current repository context |
| `wgo statusline` | Fast single-line context for shell / Claude Code prompts (local-only) |
| `wgo -C <dir> <cmd>` | Run any command as if started in `<dir>` (no `cd`) |
| `wgo to <url>` | Start a local checkout of a GitHub PR, branch, or issue |
| `wto <url>` | `cd` alias for `wgo to` |
| `wgo plan` | Display your plan file |
| `wgo plan add "reason"` | Annotate current branch with purpose |
| `wgo plan edit` | Edit plan file in $EDITOR |
| `wgo plan effort add <name> [-d text]` | Create an effort |
| `wgo plan effort link <name> <repo:bookmark>` | Add a bookmark to an effort |
| `wgo plan effort unlink <name> <repo:bookmark>` | Remove a bookmark from an effort |
| `wgo plan effort remove <name> [--force]` | Delete an effort from state and the plan |
| `wgo add "task"` | Add a task to your plan |
| `wgo done "pattern"` | Mark a matching task as complete |
| `wgo cancel "pattern"` | Cancel a matching task |
| `wgo ls` | List all discovered repositories |
| `wgo status` | Dashboard across all tracked repos |
| `wgo status --watch` | Live-updating status dashboard |
| `wgo status --filter dirty` | Show only repos with uncommitted changes |
| `wgo status --filter stale` | Show only repos with no recent activity |
| `wgo status --sort activity` | Sort repos by last commit time |
| `wgo dash` | Live web dashboard of all ongoing work, with workspace actions ([details](#live-dashboard-with-wgo-dash)) |
| `wgo dash --json` | One dashboard snapshot as JSON |
| `wgo dash --md --query EXPR` | The query's rows as Markdown with links and provenance ([details](#queries-and-markdown-extracts)) |
| `wgo setup url-handler` | Optional macOS `wgo://` link handler; `--uninstall` removes it ([details](#wgo-links-with-wgo-setup-url-handler)) |
| `wgo open <wgo://open?ws=ID>` | Open a terminal tab for a `wgo://` workspace link |
| `wgo today` | Morning review: commits, PRs, branches, tasks |
| `wgo today --yesterday` | Review the previous day |
| `wgo pr` | PRs needing attention across all repos |
| `wgo pr --mine` | Your open PRs and CI status |
| `wgo pr --review` | PRs assigned to you for review |
| `wgo contrib` | Contribution heatmap across repos |
| `wgo contrib --weeks N` | Heatmap for the past N weeks |
| `wgo clean --dry-run` | Preview what would be removed |
| `wgo clean --worktrees` | Remove worktrees for merged/closed PRs |
| `wgo clean --branches` | Remove local branches for merged PRs |
| `wgo clean --repos` | Remove unused fork checkouts |
| `wgo clean --remote` | Close draft/stale PRs on GitHub |
| `wgo track [path]` | Register a repository for tracking |
| `wgo park [repo]` | Move work stranded on a main clone into its own workspace |
| `wgo park --dry-run` | Preview the move without changing anything |
| `wgo park --name <slug>` | Override the destination slug and bookmark name |
| `wgo doctor` | Report stranded work, redundant trunk workspaces, drifted rig checkouts, spec violations |
| `wgo rig new <name> --from-binary <path>` | Create a pinned workspace from a shipped binary's build info |
| `wgo rig new <name> --from <owner/repo>@<ref>` | Create a pinned workspace from an artifact's tagged source |
| `wgo rig ls` | List rigs |
| `wgo rig show [name] [--env]` | Show a rig's modules and pins, or shell exports for `eval` |
| `wgo rig verify [name] [--all]` | Check the rig still resolves the versions the artifact shipped with |
| `wgo rig verify --freeze` | Pin drifted third-party versions back to the baseline |
| `wgo rig sync [name] [--prune]` | Re-resolve pins and bring checkouts back in line |
| `wgo rig add [name] -m <path>@<version>` | Add a module to a rig by hand |
| `wgo rig rm <name>` | Forget the rig's jj workspaces and delete its tree |
| `wgo to <url> --on <branch>` | New worktree based on `<branch>` instead of `origin/<default>` (records stack parent) |
| `wgo stack new <name>` | Register the current branch as a stack root |
| `wgo stack push <branch> --on <parent>` | Create a worktree/branch on top of a parent and (with `--draft`) open the PR |
| `wgo stack restack [<branch>]` | Rebase every descendant of `<branch>` across worktrees and push atomically |
| `wgo stack restack --continue` | Resume after resolving a rebase/merge conflict |
| `wgo stack sync` | Retarget child PR bases when parents merge and refresh marker blocks |
| `wgo stack status [<id>]` | Print the stack DAG with PR numbers and parents |
| `wgo stack adopt <name> <root> [<child>...]` | Register an existing chain of branches as a managed stack |
| `wgo stack rm <branch>` | Remove a branch from its stack (refuses if it has unmerged children) |
| `wgo hooks install` | Install global git hooks for passive monitoring |
| `wgo --version` | Show version information |
| `wgo --help` | Show help |

---

## Development

### Architecture

wgo is written in Go and follows a clean architecture pattern:

```
cmd/wgo/              # CLI entry point
internal/
  cmd/                # Command implementations (dot, plan, ls, track)
  git/                # Git client interface and implementation
  store/              # State and plan file persistence
  plan/               # Plan file parsing and rendering
  config/             # Configuration management
  discovery/          # Filesystem repository discovery
pkg/
  models/             # Shared data models
```

**Key Design Patterns:**

- **Interface-based git client** (workset pattern) for testability
- **CLI wrapper** (gwq pattern) for git operations
- **Store abstraction** for persistence layer
- **Tolerant parsing** for plan files (preserves manual edits)

### Building

```bash
# Build the binary
go build -o wgo ./cmd/wgo

# Build with version info
go build -ldflags="-X 'main.version=1.0.0'" -o wgo ./cmd/wgo

# Run without building
go run ./cmd/wgo [command]
```

### Testing

```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run tests for a specific package
go test ./internal/git
go test ./internal/plan
go test ./internal/store
go test ./internal/discovery

# Run tests with coverage
go test -cover ./...

# Generate coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

**Test Coverage:**
- 28 tests across 5 packages
- Real git repositories created in tests using `t.TempDir()`
- Round-trip testing for plan file preservation
- All git operations tested with actual git commands

### Code Quality

```bash
# Format code
go fmt ./...

# Run linter (requires golangci-lint)
golangci-lint run

# Check for common issues
go vet ./...

# Tidy dependencies
go mod tidy
```

### Local CI Hooks

If you are on Git 2.54 or newer, you can opt into repo-local CI hooks backed by
tracked scripts in this repo:

```bash
# Install into worktree-local git config (default)
./scripts/setup-local-hooks.sh install

# Or install into shared repo-local config
./scripts/setup-local-hooks.sh install --local

# Inspect configured hooks
./scripts/setup-local-hooks.sh status

# Remove them again
./scripts/setup-local-hooks.sh uninstall
```

The scripts live in `scripts/hooks/` and are checked in. The Git hook
configuration is stored in your local repository metadata, so it is not checked
in or shared via commits.

### Project Structure Details

#### internal/git/

Git client abstraction with interface and CLI implementation:

- `Client` interface with 7 methods
- `CLIClient` implementation wrapping git commands
- Context support for timeouts
- Comprehensive tests using real git repos

#### internal/store/
Persistence layer for state and plan files:
- `Store` interface with FileStore implementation
- State stored as JSON in `~/.wgo/state.json`
- Plan stored as markdown in `~/.wgo/plan.md`
- Symlink management for `~/.plan`

#### internal/plan/
Plan file parser and renderer:
- Tolerant markdown parsing; ambiguous content is kept and reported in `Plan.Diagnostics`
- Keeps the raw file lines, so `Render` rewrites only what a caller changed and
  preserves unknown sections, prose, fences and line endings byte for byte
- Manages Tasks, Active Branches, Efforts, and Notes sections
- Golden round-trip files in `testdata/roundtrip/`

#### internal/effort/
Effort references and workspace attribution:
- Resolves `repo:bookmark` against discovered main clones (one clone or an error)
- Merges state and plan efforts (the plan wins) and attributes workspaces to them
- `Collect` is read-only: it takes a `Source` that can load state and the plan but not write them

#### internal/config/
Configuration management using Viper:
- TOML format at `~/.wgo/config.toml`
- Auto-creation with sensible defaults
- Discovery settings (base_dirs, scan_depth, exclude_patterns)
- UI settings (icons, tilde_home)

#### internal/discovery/
Filesystem-based repository discovery:
- Recursive directory scanning via `filepath.Walk`
- Respects scan depth limits
- Exclude pattern matching
- Detects both regular repos and worktrees

### Adding New Commands

To add a new command:

1. Create a new file in `internal/cmd/` (e.g., `internal/cmd/mycommand.go`)
2. Define a cobra command:

```go
package cmd

import (
    "github.com/spf13/cobra"
)

var myCmd = &cobra.Command{
    Use:   "my",
    Short: "My new command",
    RunE: func(cmd *cobra.Command, args []string) error {
        return myCommandLogic()
    },
}

func init() {
    rootCmd.AddCommand(myCmd)
}

func myCommandLogic() error {
    // Your implementation here
    return nil
}
```

3. Add tests in `internal/cmd/mycommand_test.go`
4. Update this README

### Dependencies

Core dependencies:
- **cobra** — CLI framework
- **viper** — Configuration management

All dependencies managed via `go.mod` with version pinning.

### Git Workflow

```bash
# Create a feature branch
git checkout -b feature/my-feature

# Make changes and commit
git add .
git commit -m "feat: Add new feature"

# Run tests before pushing
go test ./...

# Push changes
git push origin feature/my-feature
```

**Commit Message Convention:**
Follow conventional commits:
- `feat:` — New feature
- `fix:` — Bug fix
- `chore:` — Maintenance
- `docs:` — Documentation
- `test:` — Tests only
- `refactor:` — Code refactoring

### Debugging

```bash
# Run with verbose logging
go run ./cmd/wgo -v [command]

# Build with debug symbols
go build -gcflags="all=-N -l" -o wgo ./cmd/wgo

# Use delve debugger
dlv debug ./cmd/wgo -- [command] [args]
```

### Performance Testing

```bash
# Time discovery across many repos
time ./wgo ls

# Profile CPU usage
go test -cpuprofile=cpu.prof ./internal/discovery
go tool pprof cpu.prof

# Profile memory usage
go test -memprofile=mem.prof ./internal/discovery
go tool pprof mem.prof
```

## Reference Implementations

This project draws architectural patterns from:

- **[gwq](https://github.com/d-kuro/gwq)** (d-kuro/gwq) — Clean worktree manager with status dashboard
- **[workset](https://github.com/strantalis/workset)** (strantalis/workset) — Multi-repo workspace manager with PR integration

See `CLAUDE.md` for architectural decisions and reference implementations.
