# Antigravity hooks for `wgo agent`

Antigravity can report its conversation lifecycle to `wgo agent hook --tool antigravity`.
The hook stores one entry per Antigravity `conversationId`. With one mounted
`jj` workspace it records that workspace; with several it waits for a file edit
or command directory to identify the active workspace and bookmark.
`wgo agent status`, `wgo statusline`, `wgo .`, and `wgo dash` then show that
session alongside Claude Code, Codex, and manually registered agents.

## Configuration

Add the following to `~/.gemini/config/hooks.json` (all workspaces), a plugin's
`hooks.json` (such as `~/.gemini/config/plugins/wgo/hooks.json`), or a
project's `.agents/hooks.json`:

```json
{
  "wgo-agent-session": {
    "SessionStart": [
      {
        "type": "command",
        "command": "wgo agent hook SessionStart --tool antigravity",
        "timeout": 10
      }
    ],
    "PreInvocation": [
      {
        "type": "command",
        "command": "wgo agent hook PreInvocation --tool antigravity",
        "timeout": 10
      }
    ],
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "wgo agent hook PostToolUse --tool antigravity",
            "timeout": 10
          }
        ]
      }
    ],
    "Stop": [
      {
        "type": "command",
        "command": "wgo agent hook Stop --tool antigravity",
        "timeout": 10
      }
    ]
  }
}
```

Ensure `wgo` and `jj` are on the `PATH` seen by Antigravity's hook runner
(when launching Antigravity.app from macOS LaunchServices, wrap the command in
a small script that prepends `$HOME/go/bin:/opt/homebrew/bin:/usr/local/bin` to
`PATH`).

## How it works

| Event | Status | Stdout response |
|---|---|---|
| `SessionStart` | `waiting` (refreshes workspace & bookmark) | `{}` |
| `PreInvocation` | `working` (refreshes workspace & bookmark) | `{}` (or `injectSteps` `user_markdown` warning, at most once/hour) |
| `PostToolUse` | `working` (throttled to 60s; records `Focus` on file writes and edits) | `{}` |
| `Stop` | `waiting` (refreshes workspace & bookmark) | `{"decision":"allow"}` |

- **CamelCase payloads on stdin.** Antigravity sends `conversationId`,
  `workspacePaths`, and `toolCall` (`name` and `args.TargetFile` for
  `write_to_file`, `replace_file_content`, and `multi_replace_file_content`).
  The hook event is supplied by the configured command. When `cwd` is outside a `jj`
  workspace (such as Antigravity's default scratch folder), `wgo` uses
  `workspacePaths`. If several workspaces are mounted, a `TargetFile` or a
  `run_command` `Cwd` selects the active workspace; until then, no bookmark is
  claimed for the conversation.
- **Process liveness.** `wgo` walks up the parent process tree to match
  `Antigravity`, `agy`, or Antigravity's bundled `language_server` binary, and
  records its PID and start-time token. An exited process removes its sessions.
  Because the app process can outlive a conversation, a session with no hook
  event for 10 minutes becomes **uncertain** and expires after 1 hour, even if
  Antigravity stays open. A later hook event records it again.
- **Environment detection.** Inside Antigravity (`ANTIGRAVITY_AGENT` set),
  `wgo agent start` and `wgo .` / `wgo statusline` also recognize `antigravity`
  automatically.
- **Shared `AGENTS.md`.** `wgo add`, `wgo join`, and `wgo rig` generate
  `AGENTS.md` alongside `CLAUDE.md`, so Antigravity picks up multi-repo
  workspace and rig guardrails automatically.

## Checking it

```bash
wgo agent status
wgo agent status --json
```
