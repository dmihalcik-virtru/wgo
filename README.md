# wgo

A CLI tool for tracking developer work context across your entire filesystem — branches, worktrees, PRs, and AI agent sessions across multiple repositories.

## What Problem Does This Solve?

Developers with many branches, worktrees, and repos across multiple checkouts lose track of:
- **What** branches they created
- **Why** those branches exist
- **Where** things are located on their filesystem

AI coding agents compound this problem by creating work across multiple contexts simultaneously. No existing tool maintains a local, human-readable plan file that maps branches to purpose to PR status across repositories.

**wgo fills this gap.**

## Features

### Current (Phases 1-3)

- **`wgo .`** — Instantly see your current repository context (branch, status, commits, remote tracking)
- **`wgo plan`** — View and manage a human-readable markdown plan file tracking all your work
- **`wgo plan add "reason"`** — Annotate branches with their purpose
- **`wgo ls`** — List all repositories across your filesystem with their status
- **Persistent tracking** — All annotations stored in `~/.wgo/state.json`
- **Plan file** — Human-editable markdown at `~/.plan` (symlinked from `~/.wgo/plan.md`)
- **Fast discovery** — Automatically finds repositories in configured directories

### Coming Soon (Phases 4-6)

- Real-time status dashboard with watch mode
- GitHub PR integration with cached status
- Fuzzy finder for quick worktree/branch selection
- AI agent session tracking
- Cross-repo effort grouping
- Passive git hooks for automatic updates

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
- Git 2.0 or later

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

## Commands Reference

| Command | Description |
|---------|-------------|
| `wgo .` | Show current repository context |
| `wgo plan` | Display your plan file |
| `wgo plan add "reason"` | Annotate current branch with purpose |
| `wgo plan edit` | Edit plan file in $EDITOR |
| `wgo ls` | List all discovered repositories |
| `wgo track [path]` | Register a repository for tracking |
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
- **gwq** (d-kuro/gwq) — Clean worktree manager with status dashboard
- **workset** (strantalis/workset) — Multi-repo workspace manager with PR integration

## Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Ensure all tests pass (`go test ./...`)
5. Submit a pull request

## License

[Add your license here]

## Credits

Built with inspiration from gwq and workset. See `CLAUDE.md` for architectural decisions and reference implementations.
