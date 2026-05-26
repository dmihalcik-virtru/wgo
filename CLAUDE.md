# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`wgo` is a CLI tool for tracking developer work context across the entire filesystem — branches, worktrees, PRs, and AI agent sessions across multiple repositories. It maintains a human-readable `~/.plan` markdown file as a git-versioned journal of what you're working on and why.

**Core problem:** Developers with many branches, worktrees, and repos lose track of what they created, why, and where things are. AI coding agents make this worse by creating work across multiple contexts simultaneously.

## Architecture Direction

`wgo` draws from two reference implementations checked out as subfolders:

- **`gwq/`** (d-kuro/gwq) — Clean Go worktree manager with fuzzy finder, real-time status dashboard (`status -w`), global filesystem discovery, template-based naming, tmux integration, and a registry with expiration. Well-architected, focused, excellent CLI UX. **Primary architectural reference.**
- **`workset/`** (strantalis/workset) — Sophisticated multi-repo workspace manager with full GitHub PR lifecycle, session management (tmux/screen/daemon), hooks system, workspace grouping, and a Wails desktop app. Heavier but has the cross-repo and PR integration patterns we need.

### What `wgo` adds beyond both

1. **The `.plan` file** — a git-versioned markdown journal in `~/.wgo/` that records what you're working on, why branches exist, and how efforts relate across repos
2. **Agent tracking** — record which AI tool (Claude, Codex, etc.) is working on which worktree/branch
3. **Cross-repo effort correlation** — link branches across repos that belong to the same task
4. **Passive git monitoring** — global git hooks that build context automatically as you work
5. **Branch annotations** — "why did I make this" metadata that `wgo .` displays alongside live status

### Storage Model

- `~/.plan` — user-facing markdown plan file (symlinked from `~/.wgo/plan.md`)
- `~/.wgo/` — git-versioned storage directory containing:
  - `plan.md` — the canonical plan file
  - `state.json` — runtime state (discovered repos, worktrees, agent sessions)
  - `config.toml` — user configuration
  - `cache/` — TTL-cached data (PR status, branch metadata)

### Key Commands

```
wgo .                    # Current context: branch, PR, worktree, agent status
wgo status               # Dashboard across all tracked repos/worktrees (watch mode)
wgo plan                 # Show/edit the .plan file
wgo plan add "reason"    # Annotate current branch with purpose
wgo ls                   # List all known worktrees/branches across repos
wgo track <path>         # Start tracking a repo/worktree
wgo agent status         # Show what AI agents are doing across worktrees
```

### Integration Points

- **Git** — `go-git` for read operations, shell out to `git` for worktree management. Global hooks via `core.hooksPath` for passive monitoring.
- **GitHub CLI** (`gh`) — PR status, checks, reviews. Follow workset's pattern of wrapping `gh` CLI calls.
- **Fuzzy finder** — follow gwq's pattern using `go-fuzzyfinder` for interactive selection with preview windows.
- **Terminal** — tmux integration for session management, following gwq's approach.

### Data Flow

1. Global git hooks fire on branch/worktree/commit operations → `wgo` records events in `state.json`
2. `wgo .` reads current directory's git state, merges with stored annotations and cached PR data
3. `wgo status` discovers worktrees via filesystem walk (gwq's discovery pattern), collects status in parallel
4. `wgo plan add` writes branch annotation to `.plan` file → auto-committed in `~/.wgo/`
5. PR data fetched from `gh` on demand, cached with TTL

## Development Commands

```bash
go build -o wgo ./cmd/wgo        # Build
go run ./cmd/wgo [command]        # Run locally
go test ./...                     # Test all
go test ./internal/plan           # Test single package
go install ./cmd/wgo              # Install to GOPATH/bin
```

## Reference Code Patterns

When implementing, prefer these patterns from the reference projects:

**From gwq:**

- `gwq/internal/discovery/` — filesystem-based global worktree discovery (no manual registration)
- `gwq/internal/finder/` — fuzzy finder with preview windows for worktrees, branches, sessions
- `gwq/internal/cmd/status.go` + `status_collector.go` — parallel status collection with watch mode
- `gwq/internal/registry/` — JSON registry with expiration support
- `gwq/internal/config/` — TOML config with global + local merge (local overrides global)
- `gwq/pkg/models/` — clean data models (Worktree, WorktreeStatus, GitStatus)
- `gwq/internal/template/` — template-based worktree path naming

**From workset:**

- `workset/pkg/worksetapi/` — service layer pattern for business logic
- `workset/internal/git/` — git client interface (abstract over CLI calls)
- `workset/pkg/worksetapi/` GitHub provider pattern — wraps `gh` CLI for PR operations
- `workset/internal/workspace/` — state.json pattern for persisting runtime state (current branch, PRs, sessions)
- `workset/internal/hooks/` — hook execution engine with event context variables

## Project Structure (Target)

```
cmd/wgo/              # CLI entry point (cobra)
internal/
  cmd/                # Command definitions (follow gwq's pattern)
  plan/               # Plan file parsing, rendering, and updates
  git/                # Git client interface and CLI wrapper
  github/             # GitHub CLI integration for PR status
  discovery/          # Filesystem-based repo/worktree discovery
  registry/           # Persistent tracking of repos, branches, annotations
  finder/             # Fuzzy finder for interactive selection
  status/             # Parallel status collection and dashboard
  agent/              # AI agent session tracking
  config/             # Configuration management
  store/              # Storage layer for ~/.wgo (git-versioned)
pkg/
  models/             # Shared data models
```

## Spec-Driven Development

When working on a branch whose name contains a ticket ID, **read the corresponding spec file first** before writing any code. The spec is the authoritative source of requirements, acceptance criteria, and what is explicitly out of scope. If the spec file exists, treat its Acceptance Criteria as the definition of done.

Two ticket prefixes are recognized:

- **Jira** — `[A-Z]+-\d+` prefix (e.g. `WGO-112-wgo-join` → `spec/WGO-112.md`).
- **GitHub Issues** — `gh-\d+` prefix (e.g. `gh-9-stacked-prs` → `spec/gh-9.md`).

The ticket ID is the prefix of the branch name; the spec lives at `spec/<TICKET>.md` relative to the repo root.

## Key Design Constraints

- **Read-heavy, write-light** — most operations query state; `go-git` is sufficient (no merge support needed)
- **Fast** — queries under 100ms; cache `gh` API calls with TTL
- **Non-destructive** — never modify user's repos; only read git state and maintain separate `~/.wgo` storage
- **Human-editable plan** — the `.plan` file must remain readable and manually editable markdown; parse tolerantly
- **Graceful degradation** — work without `gh`, without global hooks, without tmux; each integration is optional
