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
- **`wgo ls`** — List all repositories across your filesystem with their status
- **Persistent tracking** — All annotations stored in `~/.wgo/state.json`
- **Plan file** — Human-editable markdown at `~/.plan` (symlinked from `~/.wgo/plan.md`)
- **Fast discovery** — Automatically finds repositories in configured directories

### Jump to Any GitHub URL

- **`wgo to <url>`** — Paste a GitHub PR, branch, or issue URL and get a local worktree path back to go to
- Clones the repo if you don't have it, creates a worktree, and prints the path
- Works with `cd $(wgo to ...)` or the `wto` shell alias (see [Shell Alias](#shell-alias) below)

### Coming Soon

- GitHub PR integration with cached status
- Fuzzy finder for quick worktree/branch selection
- AI agent session tracking
- Cross-repo effort grouping

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

### Tradeoffs of pure jj

Workspaces created by `wgo add` and `wgo to` do not contain a `.git/`
directory (jj's `--no-colocate` mode). External tools that look for git
state will not work in those workspaces:

- IDE / editor git plugins that rely on libgit2 or `.git/index`
- The [`pre-commit`](https://pre-commit.com/) framework
- `direnv`'s git-aware features

If you need those tools, run them in a separate colocated checkout of
the same repo (not managed by wgo) or upstream a jj-aware adapter.

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

If the repo isn't cloned locally, `wgo to` clones it first. If a worktree for the branch already exists, it returns the existing path.

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

[ui]
# Display icons in output
icons = false

# Display home directory as ~ in output
tilde_home = true
```

Edit this file to customize discovery behavior.

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

## Commands Reference

| Command | Description |
|---------|-------------|
| `wgo .` | Show current repository context |
| `wgo to <url>` | Start a local checkout of a GitHub PR, branch, or issue |
| `wto <url>` | `cd` alias for `wgo to` |
| `wgo plan` | Display your plan file |
| `wgo plan add "reason"` | Annotate current branch with purpose |
| `wgo plan edit` | Edit plan file in $EDITOR |
| `wgo add "task"` | Add a task to your plan |
| `wgo done "pattern"` | Mark a matching task as complete |
| `wgo cancel "pattern"` | Cancel a matching task |
| `wgo ls` | List all discovered repositories |
| `wgo status` | Dashboard across all tracked repos |
| `wgo status --watch` | Live-updating status dashboard |
| `wgo status --filter dirty` | Show only repos with uncommitted changes |
| `wgo status --filter stale` | Show only repos with no recent activity |
| `wgo status --sort activity` | Sort repos by last commit time |
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
- Tolerant markdown parsing
- Preserves manual edits through round-trip
- Supports Active Branches, Efforts, and Notes sections
- Tested with complex manual edit scenarios

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
