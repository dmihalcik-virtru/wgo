# Repository Guidelines
wgo is a Go CLI for tracking local software development connected to GitHub repositories and jj workspaces, with integration with Jira and GitHub issue trackers. It maintains human-readable .plan markdown files as a versioned journal of what and why you are working on things.
**Core problem:** Developers with many branches, worktrees, and repos lose track of what they created, why, and where things are. AI coding agents make this worse by creating work across multiple contexts simultaneously. wgo helps control and manage the chaos and distractions.
## Project Structure & Module Organization
- cmd/wgo/main.go  starts the application
- Support and most packages are in `internal/
-  Shared types live in `models/`. Tests sit beside implementation files as `*_test.go`; fixture files are under package `testdata/` directories. Requirements and acceptance criteria live in `spec/`, while `internal/review/web/` contains the review graph's HTML, CSS, and JavaScript assets. `contrib/` holds optional shell integration.

## Build, Test, and Development Commands

- `go build -o wgo ./cmd/wgo` builds the CLI.
- `go run ./cmd/wgo [command]` runs it without installing a binary.
- `go test ./...` runs all package tests; `go test ./internal/jj` targets one package.
- `go vet ./...` checks for common Go mistakes.
- `gofmt -w path/to/file.go` formats edited Go files; `go mod tidy` updates module metadata when dependencies change.

Use Go 1.26 or newer. Install `jj` 0.42 or newer for runtime use and integration tests.

### Spec-Driven Development (Required for This Project)
When working on a branch in `virtru/wgo` whose name contains a ticket ID, **you MUST read the corresponding spec file first** before writing any code. The spec is the authoritative source of requirements, acceptance criteria, and what is explicitly out of scope.

- **Jira tickets:** `[A-Z]+-\d+` prefix (e.g., `WGO-112-wgo-join` → `spec/WGO-112.md`)
- **GitHub Issues:** `gh-\d+` prefix (e.g., `gh-9-stacked-prs` → `spec/gh-9.md`)

If the spec file exists, treat its Acceptance Criteria as the definition of done.

##  Design Constraints

- **Read-heavy, write-light** — most operations query state; `go-git` is sufficient (no merge support needed)
- **Fast** — queries under 100ms; cache `gh` API calls with TTL
- **Non-destructive** — never modify user's repos; only read git state and maintain separate `~/.wgo` storage
  - `wgo park` is the one deliberate exception: it rewrites the jj DAG of a main
    clone to relocate stranded work. It is opt-in (never triggered by a lookup),
    preflight-gated (every check is read-only and runs before the first
    mutation, so a rejected park leaves the operation log untouched),
    `--dry-run`-able, and rolls back over completed steps on failure. `wgo to`
    and `wgo doctor` only *report* the same condition — they never move anything.
- **Human-editable plan** — the `.plan` file must remain readable and manually editable markdown; parse tolerantly
- **Graceful degradation** — work without `gh`, without global hooks, without tmux; each integration is optional


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


`wgo` is **designed for AI-augmented development** where tools like Claude Code are first-class participants:

- **Readable State:** All persistent state is human-readable (markdown plans, JSON state files, TOML config)
- **Command Discoverability:** Commands are verb-noun (`wgo stack push`, `wgo plan add`) with consistent flags
- **Error Messages:** Errors explain what went wrong AND suggest the command to fix it
- **Spec-Driven:** Specs in `spec/*.md` provide structured context for agents to understand requirements before coding
- **Observable Actions:** `wgo .` always shows what changed (new commits, updated PRs, stack reordering)
## Coding Style & Naming Conventions
Use standard `gofmt` formatting and tabs in Go source. Keep package names short and lowercase, exported identifiers in `MixedCaps`, and tests named `TestXxx` in `*_test.go`. Put new CLI behavior in `internal/cmd/` and keep VCS operations behind `internal/jj/` rather than adding direct Git CLI calls to runtime code.

## Testing Guidelines
Add or update tests for behavior changes, preferably beside the changed package. Use `t.TempDir()` for filesystem fixtures and the existing `internal/jjtest/` helpers for jj scenarios. Run `go test ./...` before opening a PR. CI also checks `gofmt`, tidy module files, builds, vets, and runs `govulncheck`; no numeric coverage threshold is specified.

## Commit & Pull Request Guidelines
Use Gitmoji plus Conventional Commits for authored changes, for example `🐛 fix(to): handle a missing bookmark` or `📝 docs: clarify setup`. Keep each PR focused, describe the behavior change and tests run, and link its issue or ticket. For ticket branches, read the matching `spec/WGO-123.md` or `spec/gh-9.md` before coding and cover its acceptance criteria in the PR. Include screenshots when changing the review graph UI. When preparing for submitting a PR to wgo, review ./CONTRIBUTING.md for more detailed guidelines.
