# wgo — Evolutionary Delivery Plan

## Context

Developers with many branches, worktrees, and repos across multiple checkouts lose track of what they created, why, and where things are. AI coding agents compound this by creating work across contexts simultaneously. No existing tool maintains a local, human-readable plan file that maps branches to purpose to PR status across repositories.

`wgo` fills this gap as a Go CLI tool. Two reference implementations are checked out as subfolders:
- **gwq/** — primary architectural reference (worktree manager, fuzzy finder, status dashboard, filesystem discovery)
- **workset/** — secondary reference (multi-repo workspaces, GitHub PR lifecycle, state persistence, hooks)

---

## Phase 1: Foundation — "What am I working on right now?"

**User story:** *I run `wgo .` in any git repo and instantly see branch, status, remote tracking, and last commit.*

### Deliverables

| File | Purpose |
|------|---------|
| `go.mod` | Module `github.com/virtru/wgo`, Go 1.26.0. Deps: `cobra` only |
| `cmd/wgo/main.go` | Entry point → `cmd.Execute()` (pattern: `gwq/cmd/gwq/main.go`) |
| `internal/cmd/root.go` | Cobra root command, version from build info (pattern: `gwq/internal/cmd/root.go`) |
| `internal/cmd/dot.go` | `wgo .` — detect git repo, print context summary |
| `internal/git/git.go` | `Client` interface + `CLIClient` impl. Interface-based (workset pattern) with thin CLI wrapper (gwq pattern) |
| `internal/git/git_test.go` | Tests using `t.TempDir()` with real git repos |
| `pkg/models/models.go` | `GitStatus`, `CommitInfo`, `BranchInfo` structs |

### Output format for `wgo .`

```
repo:   virtru/wgo
branch: feat/plan-parser
status: 3 modified, 1 staged, 2 untracked
remote: ↑2 ↓0 (origin/feat/plan-parser)
commit: abc1234 Add initial plan parser (2 hours ago)
```

### Git client interface (initial, ~7 methods)

```go
type Client interface {
    IsRepo(path string) (bool, error)
    CurrentBranch(repoPath string) (string, error)
    Status(repoPath string) (GitStatus, error)
    AheadBehind(repoPath, branch string) (ahead int, behind int, err error)
    LastCommit(repoPath string) (CommitInfo, error)
    RepoName(repoPath string) (string, error)
    RemoteURL(repoPath string) (string, error)
}
```

### Verify

- `go build -o wgo ./cmd/wgo && ./wgo --version`
- `./wgo .` in this repo shows branch/status/commit
- `./wgo .` outside a git repo prints informative message
- `go test ./...` passes

### Reference files

- `gwq/cmd/gwq/main.go`, `gwq/internal/cmd/root.go` — CLI bootstrap
- `gwq/internal/git/git.go` — `run()`/`runWithContext()` helpers, porcelain parsing
- `workset/internal/git/git.go` — interface definition pattern
- `gwq/pkg/models/models.go` — model struct patterns

---

## Phase 2: Storage + Plan File + Branch Annotations

**User story:** *I run `wgo plan add "fixing auth token refresh"` and my current branch gets annotated. `wgo plan` shows a readable markdown file listing all annotated branches. I can edit `~/.plan` by hand and my edits survive.*

### Deliverables

| File | Purpose |
|------|---------|
| `internal/store/store.go` | `Store` interface: `LoadState`/`SaveState`/`LoadPlan`/`SavePlan`/`EnsureDir`. `FileStore` impl for `~/.wgo/` |
| `internal/store/state.go` | `State` struct: version, repos map, annotations map, efforts map, agent sessions map |
| `internal/plan/plan.go` | Plan file parser/renderer. Parse `## Active Branches` sections, preserve freeform content |
| `internal/plan/plan_test.go` | Round-trip tests: parse → modify → render preserves manual edits |
| `internal/cmd/plan.go` | `wgo plan` (display), `wgo plan add "reason"` (annotate), `wgo plan edit` (open `$EDITOR`) |
| Update `internal/cmd/dot.go` | Show annotation as `why:` line when present |

### Plan file format (`~/.wgo/plan.md`, symlinked to `~/.plan`)

```markdown
# Plan

## Active Branches

### virtru/wgo

- **feat/plan-parser** — Add initial plan file parsing
  - Created: 2026-02-16
- **fix/auth-refresh** — Fixing auth token refresh bug
  - Created: 2026-02-15

## Notes

(user freeform notes preserved here)
```

### State schema (`~/.wgo/state.json`)

```json
{
  "version": 1,
  "repos": { "/path/to/repo": { "remote_url": "...", "last_seen": "..." } },
  "annotations": {
    "/path/to/repo:branch-name": {
      "purpose": "fixing auth token refresh",
      "created_at": "...", "updated_at": "..."
    }
  }
}
```

### Verify

- `wgo plan add "fixing the auth bug"` creates `~/.wgo/state.json` and `~/.wgo/plan.md`
- `ls -la ~/.plan` shows symlink to `~/.wgo/plan.md`
- Manual edits to `~/.plan` survive subsequent `wgo plan add` on a different branch
- `wgo .` now shows `why:` line for annotated branches
- `go test ./internal/plan/...` and `go test ./internal/store/...` pass

### Reference files

- `workset/internal/workspace/workspace.go` — `loadState()`/`saveState()` JSON persistence pattern
- `workset/pkg/worksetapi/stores.go` — store interface pattern

---

## Phase 3: Discovery + Tracking + List

**User story:** *I run `wgo ls` and see all my git repos and worktrees found across configured directories, with annotations showing why each branch exists. I can also manually track repos with `wgo track`.*

### Deliverables

| File | Purpose |
|------|---------|
| `internal/config/config.go` | Viper + TOML config. Global `~/.wgo/config.toml` with `discovery.base_dirs`, `discovery.scan_depth`, `discovery.exclude_patterns`, `ui.*` |
| `internal/discovery/discovery.go` | `filepath.Walk` from base dirs. Detect both `.git` directories (repos) AND `.git` files with `gitdir:` (worktrees) |
| `internal/discovery/discovery_test.go` | Tests with temp directory trees containing repos and worktrees |
| `internal/cmd/ls.go` | `wgo ls` — tabular output with repo, branch, why, status columns |
| `internal/cmd/track.go` | `wgo track <path>` — register a repo in state.json |
| Update `pkg/models/models.go` | Add `Worktree` model (path, branch, commit hash, is_main, created_at) |

### Default config (`~/.wgo/config.toml`)

```toml
[discovery]
base_dirs = ["~/Documents/GitHub"]
scan_depth = 4
exclude_patterns = ["node_modules", ".cache", "vendor", "dist"]

[ui]
icons = false
tilde_home = true
```

### `wgo ls` output

```
REPO                BRANCH              WHY                              STATUS
virtru/wgo          feat/plan-parser    Add initial plan file parsing    3 modified
virtru/wgo          fix/auth-refresh    Fixing auth token refresh bug    clean
virtru/other-repo   main                —                                clean
```

### Verify

- `wgo track .` registers current repo in `~/.wgo/state.json`
- `wgo ls` shows tracked repos + discovered repos with branches and annotations
- Config file `~/.wgo/config.toml` created with defaults on first run
- `go test ./internal/discovery/...` passes

### Reference files

- `gwq/internal/discovery/discovery.go` — `filepath.Walk`, `.git` file parsing, URL extraction
- `gwq/internal/config/config.go` — viper two-tier config, defaults, path expansion
- `gwq/internal/cmd/list.go` — list command pattern
- `gwq/internal/registry/registry.go` — JSON registry with mutex

---

## Phase 4: Parallel Status Dashboard

**User story:** *I run `wgo status` and see a live-updating dashboard of all repos/worktrees with git status collected in parallel. I can filter by status and sort by activity.*

### Deliverables

| File | Purpose |
|------|---------|
| `internal/status/collector.go` | `CollectAll()` with `sync.WaitGroup` + goroutine per worktree, 5s context timeout per git op |
| `internal/status/collector_test.go` | Tests with mock git client |
| `internal/cmd/status.go` | `wgo status` with `--watch`/`--interval`/`--filter`/`--sort`/`--json` flags |
| `internal/cmd/status_output.go` | Table/JSON formatting with lipgloss styling |
| Add dep: `github.com/charmbracelet/lipgloss` | Terminal styling |
| Update `pkg/models/models.go` | Add `WorktreeStatus`, `WorktreeState` enum |

### Dashboard output

```
BRANCH              STATUS     CHANGES          WHY                           ACTIVITY
feat/plan-parser    changed    3 modified       Add plan file parsing         2 min ago
fix/auth-refresh    clean      —                Fixing auth token refresh     3 hours ago
main                clean      —                —                             1 day ago

Total: 3 | Changed: 1 | Clean: 2 | Inactive: 0
```

Watch mode: `wgo status -w` refreshes every 5s, clears and redraws.

### Verify

- `wgo status` shows tabular status for all tracked/discovered repos
- `wgo status -w` auto-refreshes
- `wgo status --filter modified` filters correctly
- `wgo status --json` produces valid JSON
- Completes in <2s for 10 repos (parallel collection)
- `go test ./internal/status/...` passes

### Reference files

- `gwq/internal/cmd/status_collector.go` — `CollectAll()` goroutine pattern, `collectGitStatus()` porcelain parsing, `getLastActivity()`, stale detection
- `gwq/internal/cmd/status.go` — watch mode with ANSI cursor control, signal handling
- `gwq/internal/cmd/status_output.go` — table formatting

---

## Phase 5: GitHub PR Integration + Fuzzy Finder

**User story:** *`wgo .` now shows PR status alongside branch info. I can fuzzy-search across all worktrees and jump to one. PR data is cached so repeated queries are fast.*

### Deliverables

| File | Purpose |
|------|---------|
| `internal/github/github.go` | `Client` interface: `ListPullRequests`, `GetPullRequest`, `ListCheckRuns` |
| `internal/github/cli.go` | Impl wrapping `gh` via `go-gh/v2` REST client |
| `internal/github/cache.go` | TTL file cache in `~/.wgo/cache/` (5min default) |
| `internal/finder/finder.go` | `SelectWorktree`, `SelectBranch` with preview windows showing annotation + status |
| `internal/cmd/cd.go` | `wgo cd` — fuzzy select a worktree and cd into it |
| Update `internal/cmd/dot.go` | Show `pr:` line with number, title, state, checks summary |
| Update `internal/cmd/status.go` | Add PR column to dashboard |
| Add deps: `go-gh/v2`, `go-fuzzyfinder` | GitHub + fuzzy finder |

### Enhanced `wgo .` output

```
repo:   virtru/wgo
branch: feat/plan-parser
why:    Add initial plan file parsing
status: 3 modified, 1 staged
pr:     #42 "Add plan file parser" (open, 2/2 checks ✓, 1 review pending)
```

### Verify

- `wgo .` shows PR info when branch has an open PR
- `wgo .` without `gh` installed still shows branch info (graceful degradation)
- Second `wgo .` within 5min uses cached PR data
- `wgo cd` opens fuzzy finder, selecting a worktree prints the path (shell integration via `cd $(wgo cd)`)
- `go test ./internal/github/...` passes

### Reference files

- `workset/pkg/worksetapi/github_provider_cli.go` — `go-gh/v2` REST client setup
- `workset/pkg/worksetapi/github_service.go` — PR lookup by head branch
- `gwq/internal/finder/finder.go` — `go-fuzzyfinder` with preview windows, lazy loading

---

## Phase 6: Agent Tracking + Cross-Repo Efforts + Git Hooks

**User story:** *`wgo agent status` shows which AI tools are active across my worktrees. I can group branches across repos into "efforts" and see them in my plan file. Git hooks passively update my plan as I work.*

### Deliverables

| File | Purpose |
|------|---------|
| `internal/agent/agent.go` | `AgentSession` model, detection registry |
| `internal/agent/detect.go` | Detect agents by: process scan (`ps`/`lsof` for claude/codex/cursor/aider CWDs), marker dirs (`.claude/`, `.cursor/`), manual registration |
| `internal/cmd/agent.go` | `wgo agent status`, `wgo agent register --tool claude`, `wgo agent unregister` |
| `internal/cmd/effort.go` | `wgo effort create "name"`, `wgo effort link`, `wgo effort show`, `wgo effort ls` |
| `internal/hooks/hooks.go` | Git hook scripts for `post-checkout`, `post-commit`, `post-merge`. `wgo hooks install` sets up `core.hooksPath` or drops into global hooks dir |
| Update `internal/plan/plan.go` | Render `## Efforts` section grouping cross-repo branches |
| Update `internal/store/state.go` | `Effort` and `AgentSession` already in state schema, now populated |

### Effort in plan file

```markdown
## Efforts

### auth-redesign
Redesigning the authentication flow across services.

- virtru/wgo: feat/auth-redesign (3 modified)
- virtru/api-server: feat/auth-v2 (clean, PR #45 open)
```

### Verify

- `wgo agent status` detects a running Claude Code session in a tracked worktree
- `wgo effort create "auth-redesign" && wgo effort link` creates and links
- `wgo plan` shows efforts section
- After `wgo hooks install`, branch switching auto-updates plan
- `go test ./internal/agent/...` and `go test ./internal/hooks/...` pass

### Reference files

- `workset/pkg/worksetapi/agent_types.go`, `agent_status.go` — agent integration patterns
- `workset/internal/hooks/engine.go`, `types.go`, `context.go` — hook execution engine
- `gwq/internal/cmd/status_collector.go:494-506` — process detection stub to implement

---

## Cross-Cutting Patterns

**Dependency injection:** Follow workset's `Service` pattern — `Options` struct with nil defaults auto-filled in constructor. Enables test mocking.

**Error handling:** Graceful degradation everywhere. Missing `gh`? Skip PR info. Missing hooks? Just don't auto-update. Never crash on optional enrichment.

**Testing:** Each phase includes tests. Use `t.TempDir()` with real `git init` for git wrapper tests. Interface-based git client allows mock injection for status/discovery tests.

**Dependencies added per phase:**
1. `cobra`
2. (none)
3. `viper`
4. `lipgloss`
5. `go-gh/v2`, `go-fuzzyfinder`
6. (none)
