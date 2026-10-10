
### Storage Model

- `~/.plan` — user-facing markdown plan file (symlinked from `~/.wgo/plan.md`)
- `~/.wgo/` — storage directory containing:
  - `plan.md` — the canonical plan file
  - `state.json` — runtime state (discovered repos, worktrees, agent sessions)
  - `config.toml` — user configuration
  - `cache/` — TTL-cached data: PR status and branch metadata, Jira tickets,
    GitHub issues (`cache/ghissue/<owner>/<repo>/`), and the dashboard's last
    snapshot and "since last look" baseline (`cache/dash/`)

