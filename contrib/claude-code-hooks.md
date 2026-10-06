# Claude Code hooks for `wgo agent`

`wgo agent hook <event>` keeps a Claude Code session's `wgo` agent session
accurate: it is created on the first event, marked **working** while Claude
uses tools, **waiting** when Claude stops for input, and removed when the
session ends. Several Claude sessions in one workspace each get their own
entry, keyed by Claude's `session_id`.

## Configuration

Add this to `~/.claude/settings.json` (all projects) or a project's
`.claude/settings.json`, merging with any `hooks` you already have:

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [{ "type": "command", "command": "wgo agent hook SessionStart", "timeout": 10 }] }
    ],
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "wgo agent hook UserPromptSubmit", "timeout": 10 }] }
    ],
    "PostToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "wgo agent hook PostToolUse", "timeout": 10 }] }
    ],
    "Notification": [
      { "hooks": [{ "type": "command", "command": "wgo agent hook Notification", "timeout": 10 }] }
    ],
    "Stop": [
      { "hooks": [{ "type": "command", "command": "wgo agent hook Stop", "timeout": 10 }] }
    ],
    "SessionEnd": [
      { "hooks": [{ "type": "command", "command": "wgo agent hook SessionEnd", "timeout": 10 }] }
    ]
  }
}
```

The minimum useful set is `PostToolUse`, `Stop` and `SessionEnd`; the others
make the status more precise.

| Event | Effect |
|---|---|
| `SessionStart`, `Stop`, `Notification` | waiting for input |
| `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `SubagentStop` | working |
| `SessionEnd` | session removed |
| anything else | activity refreshed, status unchanged |

`SessionStart`, `UserPromptSubmit` and `Stop` also re-read the workspace and
bookmark from the payload's `cwd`. `PostToolUse` only refreshes activity, and
repeated events with no status change within 60 seconds skip the write, so the
per-tool cost stays small.

## How it works and why it is safe

- **Hook data stays on stdin.** The command line is fixed; Claude Code's JSON
  is decoded from stdin and only `session_id`, `cwd` and `hook_event_name` are
  used. Nothing from the payload is placed on a command line or handed to a
  shell. Session IDs are restricted to `[A-Za-z0-9._:-]`.
- **Quiet output.** The hook prints nothing on stdout (for `SessionStart` and
  `UserPromptSubmit`, Claude Code would add stdout to the conversation).
- **It never breaks your session.** Malformed JSON or a missing `session_id`
  exits non-zero, which Claude Code shows as a non-blocking hook error. Every
  other failure, such as a `cwd` outside a jj workspace or an unwritable
  state file, is swallowed and exits 0. Run with `WGO_DEBUG=1` to see it.
- **Liveness without timers.** On the first event wgo walks up from the hook's
  parent process to the `claude` process and records its PID and start time.
  While that process is running, the session stays listed however long Claude
  works quietly. If the PID is later reused by another process, the start
  time no longer matches and the session is not treated as live. When no
  `claude` process is found, no identity is recorded, and after 10 quiet
  minutes the session is shown as *uncertain* rather than deleted; it is
  removed after 24 hours, by `SessionEnd`, or by `wgo agent stop`.

## Alongside the statusline

If `contrib/statusline.sh` (or `wgo .`) also runs inside Claude Code, its
heartbeat refreshes the hook-managed session for the same workspace instead of
adding a second one, and never overwrites the status the hooks set. Without
hooks, the statusline keeps a single *inferred* session per workspace, which
expires 10 minutes after the statusline stops rendering.

## Mixed wgo versions

Hooks run whichever `wgo` is first on Claude Code's `PATH`. A `wgo` that finds
state written by a newer version refuses to write it and asks you to upgrade.
Builds from before session IDs (state version 2) predate that guard, so make
sure an old binary is not shadowing the current one: `which -a wgo`.

## Checking it

```bash
wgo agent status          # every session, with liveness and conflicts
wgo agent status --json   # the same, machine-readable
```

To try the hook by hand without Claude Code:

```bash
printf '%s' '{"session_id":"test-1","hook_event_name":"PostToolUse","cwd":"'"$PWD"'"}' \
  | wgo agent hook PostToolUse
wgo agent status
wgo agent stop --session test-1
```

## Other agents and scripts

Tools without Claude Code-style hooks can manage sessions explicitly:

```bash
id=$(wgo agent start codex --theme gh-72 --json | jq -r .id)
wgo agent heartbeat --session "$id" --status waiting
wgo agent stop --session "$id"
```

Pass `--pid` to `wgo agent start` when the tool can report its own process ID.
