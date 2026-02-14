# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`wgo` (working/git organizer) is a CLI tool for managing git branches, worktrees, and tracking work across multiple repositories. It helps developers who maintain many branches and worktrees by providing a centralized `.plan` file that tracks:
- What branches exist and why they were created
- Where worktrees are located on the filesystem
- PR status for each branch
- Current work context

## Architecture

### Core Concepts

**Storage Model:**
- Primary data file: `~/.plan` (user's plan file in markdown format)
- Storage directory: `~/.wgo/` (git-versioned metadata and state)
- The `.plan` file is git-versioned within `~/.wgo/` to maintain history

**Integration Points:**
- Git: Hooks into git operations to automatically track branch creation, worktree changes
- GitHub CLI (`gh`): Queries PR status and metadata
- Filesystem: Tracks worktree locations

**Command Pattern:**
- `wgo .` - Shows current git context (branch, PR status, worktree location)
- Deterministic operations that query and update the `.plan` file
- All operations are idempotent and safe to re-run

### Data Flow

1. User performs git operations (branch creation, worktree add, etc.)
2. Git hooks notify `wgo` of changes
3. `wgo` updates `~/.plan` with new information
4. Changes are committed to the `~/.wgo/` git repository
5. User queries current state with `wgo` commands
6. `wgo` fetches live data (PR status from `gh`) and merges with stored plan

## Development Commands

**Build:**
```bash
go build -o wgo ./cmd/wgo
```

**Run locally:**
```bash
go run ./cmd/wgo [command]
```

**Test:**
```bash
go test ./...
```

**Test single package:**
```bash
go test ./internal/plan
```

**Install locally:**
```bash
go install ./cmd/wgo
```

## Key Implementation Considerations

**Git Hook Installation:**
The tool needs to install git hooks globally (via `git config --global core.hooksPath`) or provide commands to install hooks per-repository. Hooks should be non-intrusive and fail gracefully.

**Plan File Format:**
The `~/.plan` markdown file should be human-readable and editable. The tool should parse it without strict schema requirements, allowing users to add their own notes.

**State Management:**
`~/.wgo/` contains:
- Git repository for versioning the `.plan` file
- Cached data (worktree locations, last known branch states)
- Configuration files

**Cross-Repository Tracking:**
The tool tracks multiple git repositories. It needs to maintain a mapping of:
- Repository path → branches → worktrees
- Repository path → PRs

**Performance:**
Operations should be fast (<100ms for queries). Cache expensive operations like `gh` API calls with appropriate TTLs.

## Project Structure (Target)

```
cmd/wgo/          # CLI entry point
internal/
  plan/           # Plan file parsing and updates
  git/            # Git integration and hooks
  github/         # GitHub CLI integration
  store/          # Storage layer for ~/.wgo
  track/          # Branch and worktree tracking
```
