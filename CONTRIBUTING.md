## Contributing

Contributions welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Ensure all tests pass (`go test ./...`)
5. Optionally install local CI hooks with `./scripts/setup-local-hooks.sh install`
6. Submit a pull request
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

### Key Commands

```
wgo .                    # Current context: branch, PR, worktree, agent status
wgo status               # Dashboard across all tracked repos/worktrees (watch mode)
wgo plan                 # Show/edit the .plan file
wgo plan add "reason"    # Annotate current branch with purpose
wgo ls                   # List all known worktrees/branches across repos
wgo track <path>         # Start tracking a repo/worktree
wgo agent status         # Show what AI agents are doing across worktrees
wgo park                 # Move work stranded on a main clone into its own workspace
wgo doctor               # Report stranded work, redundant trunk workspaces, spec violations
```

### Worktree layout

`wgo to` resolves a target to exactly one of these:

- `<mains_dir>/<owner>/<repo>` — the clone, checked out on trunk. A bare repo
  URL or an explicit trunk URL resolves here; the clone already *is* the trunk
  checkout, so wgo never creates a second copy of it under `worktrees_dir`.
- `<worktrees_dir>/<slug>/<repo>` — one workspace per branch or issue.
- `<worktrees_dir>/pr-<N>-<slug>/<owner>/<repo>` — one workspace per PR.

### Integration Points

- **jj** — `internal/jj` shells out to the system `jj` binary (>= 0.42) for every VCS operation: workspaces, bookmarks, the change DAG, and git interop (`jj git fetch/push/init/clone/remote`). No `git` CLI calls anywhere in the runtime.
- **GitHub HTTP API** — `internal/github` talks to `https://api.github.com` directly using `net/http`. The only `gh` CLI shell-out is `gh auth token` in `internal/github/auth.go`, used as a fallback when `GITHUB_TOKEN` is unset.
- **Fuzzy finder** — follow gwq's pattern using `go-fuzzyfinder` for interactive selection with preview windows.
- **Terminal** — tmux integration for session management, following gwq's approach.
- **`gh stack` (native GitHub Stacks)** — `wgo sync` optionally publishes jj-derived stack topology to GitHub's native Stack via **`gh stack link`** (a stateless publishing call — it creates/updates the Stack without writing `.git/gh-stack` and without running git rebases). Gated by `sync.gh_stack` (`auto`/`on`/`off`) and `internal/sync.Linker.Available()`.

### Stacked PRs with `gh stack` — agent safety

`wgo` treats the stack as its unit of work and keeps jj authoritative for topology. When working in a wgo/jj workspace:

- jj creates and rewrites changes; `wgo sync` derives the topology from the jj DAG and publishes it with `gh stack link`. `wgo sync --create-prs` opens draft PRs for bookmarks that lack one — it considers *every* bookmark in the repo's DAG, so in a repo that also holds unrelated efforts' bookmarks, scope it with `--bookmark` and check with `--dry-run` first.
- **Never run `gh stack {init,add,rebase,sync,modify,submit}`** in a wgo/jj workspace. Those commands write `.git/gh-stack` shadow state and drive `git rebase`/`git push`, which fights jj's automatic descendant restacking and creates a second, drifting source of truth. Only `gh stack link` is safe, and `wgo` invokes it for you.
- To build on someone else's stack: `wgo to <PR-URL>` fetches the whole stack; `jj new <node>` forks atop a leaf or interior node; `wgo sync --create-prs` opens your PR based on the forked-from node.

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
  - `wgo park` is the one deliberate exception: it rewrites the jj DAG of a main
    clone to relocate stranded work. It is opt-in (never triggered by a lookup),
    preflight-gated (every check is read-only and runs before the first
    mutation, so a rejected park leaves the operation log untouched),
    `--dry-run`-able, and rolls back over completed steps on failure. `wgo to`
    and `wgo doctor` only *report* the same condition — they never move anything.
- **Human-editable plan** — the `.plan` file must remain readable and manually editable markdown; parse tolerantly
- **Graceful degradation** — work without `gh`, without global hooks, without tmux; each integration is optional
