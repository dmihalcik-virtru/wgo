
### ### Storage Model

- `~/.plan` — user-facing markdown plan file (symlinked from `~/.wgo/plan.md`)
- `~/.wgo/` — git-versioned storage directory containing:
  - `plan.md` — the canonical plan file
  - `state.json` — runtime state (discovered repos, worktrees, agent sessions)
  - `config.toml` — user configuration
  - `cache/` — TTL-cached data (PR status, branch metadata)

