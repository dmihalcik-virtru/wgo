

## Key Commands

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
