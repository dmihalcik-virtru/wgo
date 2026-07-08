# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`wgo` is a CLI tool for tracking developer work context across the entire filesystem — branches, worktrees, PRs, and AI agent sessions across multiple repositories. It maintains a human-readable `~/.plan` markdown file as a git-versioned journal of what you're working on and why.

**Core problem:** Developers with many branches, worktrees, and repos lose track of what they created, why, and where things are. AI coding agents make this worse by creating work across multiple contexts simultaneously.

## Product Vision: What `wgo` Enables

`wgo` is designed to keep developers **focused and in flow** by reducing context switching, automating tedious tasks, and surfacing exactly the information needed at the moment it's needed. It is **workflow-agnostic** — supporting but never requiring specific commit conventions, PR workflows, or development practices.

### Core Product Principles

1. **Minimize Context Switching**
   - **Quick Links:** All output uses OSC8 terminal hyperlinks (via `internal/links/`) so developers can click directly from the CLI to PRs, commits, specs, issues, or repos without copying URLs or switching windows
   - **At-a-Glance Status:** `wgo .` shows everything about the current context in one screen: branch, PR status, stack position, related spec, active tasks
   - **Unified View:** `wgo status` and `wgo pr` aggregate information across repos/branches so you don't hunt through multiple tabs

2. **Automation Without Opinion**
   - **DAG-Backed History:** `wgo` reads the jj operation log and change DAG directly — no separate hook system or shadow state for tracking commit/bookmark activity
   - **Auto-Discovery:** Filesystem scanning finds repos and workspaces (jj's worktree equivalent) without manual registration
   - **Smart Defaults:** Commands infer intent from context (e.g., `wgo sync` resolves the current bookmark from the workspace's `@`)
   - **Graceful Degradation:** Work without a `GITHUB_TOKEN`, without `gh`, without tmux — each integration is optional

3. **AI Agent Integration**
   - **Claude Code First-Class Support:** Designed for workflows where Claude Code (or other AI agents) create branches, worktrees, and PRs
   - **Agent Session Tracking:** Record which AI tool is working on which worktree/branch via `wgo agent status`
   - **Markdown-Native:** The `.plan` file and spec files are markdown so agents can read/write them naturally
   - **Command Consistency:** Simple, composable commands that agents can chain together (e.g., `wgo to --on parent-branch && wgo stack push child-branch --draft`)

4. **Developer Productivity Over Purity**
   - **Fast Over Perfect:** Better to show cached PR data in 50ms than wait 2s for fresh data
   - **No Forced Workflows:** Support conventional commits and gitmoji but never require them — `wgo` works with any commit style
   - **Escape Hatches:** Allow manual edits to `.plan`, work without GitHub integration, function without PRs
   - **Feedback Loops:** Show CI status, PR review state, merge conflicts immediately — don't make developers check GitHub

### Features `wgo` Will Support (Not Require)

These are capabilities `wgo` will recognize and enhance, but users can ignore them entirely:

- **Gitmoji Recognition** — `wgo .` and `wgo status` will parse and display gitmoji if present in commits, but won't complain if absent
- **Conventional Commit Parsing** — `wgo` will extract `type(scope)` from commits for better categorization in logs/dashboards, but will display any commit format
- **Spec-first Development** — `wgo .` will link to spec files when ticket IDs are in branch names, but won't block work without specs
- **PR Templates** — `wgo stack push --draft` can pre-fill PR bodies with templates, but will work fine without them
- **Commit Linking** — `wgo` will detect and hyperlink `Refs: #123` footers, but won't require them

### Integration with Coding Agents

`wgo` is **designed for AI-augmented development** where tools like Claude Code are first-class participants:

- **Readable State:** All persistent state is human-readable (markdown plans, JSON state files, TOML config)
- **Command Discoverability:** Commands are verb-noun (`wgo stack push`, `wgo plan add`) with consistent flags
- **Error Messages:** Errors explain what went wrong AND suggest the command to fix it
- **Spec-Driven:** Specs in `spec/*.md` provide structured context for agents to understand requirements before coding
- **Observable Actions:** `wgo .` always shows what changed (new commits, updated PRs, stack reordering)

## Development Practices for This Project

**These rules apply to developing `wgo` itself, NOT to projects that use `wgo`.** Contributors to the `virtru/wgo` repository must follow these standards:

### Required Commit Standards

All commits to `virtru/wgo` **MUST** follow both Conventional Commits and Gitmoji:

**Format:**
```
<gitmoji> <type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

**Allowed types:**
- `feat` — New feature
- `fix` — Bug fix
- `docs` — Documentation changes
- `style` — Code style changes (formatting, missing semicolons, etc.)
- `refactor` — Code refactoring (neither fixes a bug nor adds a feature)
- `perf` — Performance improvements
- `test` — Adding or updating tests
- `chore` — Build process, tooling, dependencies, or housekeeping
- `ci` — CI/CD configuration changes

**Common gitmoji (see [gitmoji.dev](https://gitmoji.dev/) for full list):**
- ✨ `:sparkles:` — Introduce new features
- 🐛 `:bug:` — Fix a bug
- 📝 `:memo:` — Add or update documentation
- 🎨 `:art:` — Improve structure/format of code
- ⚡ `:zap:` — Improve performance
- 🔥 `:fire:` — Remove code or files
- ✅ `:white_check_mark:` — Add, update, or pass tests
- ♻️ `:recycle:` — Refactor code
- 🔧 `:wrench:` — Add or update configuration files

**Examples:**
```
✨ feat(stack): add PR number links to wgo . output
🐛 fix(github): handle missing PR gracefully in stack display
📝 docs(claude): document project goals and automation philosophy
✅ test(stack): add test coverage for PR link generation
♻️ refactor(cmd): extract showStackLine parameters to struct
```

### Spec-Driven Development (Required for This Project)

When working on a branch in `virtru/wgo` whose name contains a ticket ID, **you MUST read the corresponding spec file first** before writing any code. The spec is the authoritative source of requirements, acceptance criteria, and what is explicitly out of scope.

- **Jira tickets:** `[A-Z]+-\d+` prefix (e.g., `WGO-112-wgo-join` → `spec/WGO-112.md`)
- **GitHub Issues:** `gh-\d+` prefix (e.g., `gh-9-stacked-prs` → `spec/gh-9.md`)

If the spec file exists, treat its Acceptance Criteria as the definition of done.

## Architecture Direction

`wgo` draws from two reference implementations checked out as subfolders:

- **`gwq/`** (d-kuro/gwq) — Clean Go worktree manager with fuzzy finder, real-time status dashboard (`status -w`), global filesystem discovery, template-based naming, tmux integration, and a registry with expiration. Well-architected, focused, excellent CLI UX. **Primary architectural reference.**
- **`workset/`** (strantalis/workset) — Sophisticated multi-repo workspace manager with full GitHub PR lifecycle, session management (tmux/screen/daemon), hooks system, workspace grouping, and a Wails desktop app. Heavier but has the cross-repo and PR integration patterns we need.

### What `wgo` adds beyond both

1. **The `.plan` file** — a versioned markdown journal in `~/.wgo/` that records what you're working on, why bookmarks exist, and how efforts relate across repos
2. **Agent tracking** — record which AI tool (Claude, Codex, etc.) is working on which workspace/bookmark
3. **Cross-repo effort correlation** — link bookmarks across repos that belong to the same task
4. **Branch annotations** — "why did I make this" metadata that `wgo .` displays alongside live status

> **VCS backend:** `wgo` uses [jj (Jujutsu)](https://github.com/jj-vcs/jj)
> as its only VCS backend. The git/`go-git`/hook integration from the
> early prototypes has been removed; see `spec/gh-21.md` and
> `spec/gh-21-b.md` for the migration history. The repo `wgo` itself is
> tracked with git for contributor convenience (CI, PR workflow).

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

- **jj** — `internal/jj` shells out to the system `jj` binary (>= 0.42) for every VCS operation: workspaces, bookmarks, the change DAG, and git interop (`jj git fetch/push/init/clone/remote`). No `git` CLI calls anywhere in the runtime.
- **GitHub HTTP API** — `internal/github` talks to `https://api.github.com` directly using `net/http`. The only `gh` CLI shell-out is `gh auth token` in `internal/github/auth.go`, used as a fallback when `GITHUB_TOKEN` is unset.
- **Fuzzy finder** — follow gwq's pattern using `go-fuzzyfinder` for interactive selection with preview windows.
- **Terminal** — tmux integration for session management, following gwq's approach.

### Data Flow

1. `wgo .` / `wgo status` read the current workspace's bookmark and parent change via `jj log -T <template>` and merge with stored annotations + cached PR data
2. `wgo status` discovers workspaces via filesystem walk (gwq's discovery pattern, adapted to scan for `.jj/` instead of `.git/`), collects status in parallel
3. `wgo plan add` writes branch annotation to `.plan` file → committed in `~/.wgo/`
4. PR data fetched from the GitHub REST API on demand, cached with TTL via the per-client transport in `internal/github/transport.go`

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

## Key Design Constraints

- **Read-heavy, write-light** — most operations query state; `go-git` is sufficient (no merge support needed)
- **Fast** — queries under 100ms; cache `gh` API calls with TTL
- **Non-destructive** — never modify user's repos; only read git state and maintain separate `~/.wgo` storage
- **Human-editable plan** — the `.plan` file must remain readable and manually editable markdown; parse tolerantly
- **Graceful degradation** — work without `gh`, without global hooks, without tmux; each integration is optional
