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
- **It never breaks your session.** Malformed JSON, a missing or invalid
  `session_id`, or no event exits non-zero, which Claude Code shows as a
  non-blocking hook error. Everything else exits 0. A `cwd` outside a jj
  workspace is skipped silently (run with `WGO_DEBUG=1` to see it). A state
  file wgo cannot read or write, such as one from a newer wgo, is reported
  once on stderr, because it means sessions are not being tracked.
- **Liveness without timers.** wgo walks up from the hook's parent process to
  the `claude` process and records its PID and start time; it does this on
  each event until the process is found, and again when the recorded one has
  exited (`claude --resume` keeps the session but starts a new process). While
  that process is running, the session stays listed however long Claude works
  quietly. After 10 quiet minutes, a session whose process has exited, or
  whose PID now belongs to a different process (the start time no longer
  matches), is removed. A session with no recorded process, or one whose
  process cannot be checked, is shown as *uncertain* rather than deleted; it
  is removed after 24 hours, by `SessionEnd`, or by `wgo agent stop`.

## Alongside the statusline

If `contrib/statusline.sh` (or `wgo .`) also runs inside Claude Code, its
heartbeat refreshes the hook-managed session for the same workspace instead of
adding a second one, and never overwrites the status the hooks set. Without
hooks, the statusline keeps a single *inferred* session per workspace, which
expires 10 minutes after the statusline stops rendering.

## Mixed wgo versions

Hooks run whichever `wgo` is first on Claude Code's `PATH`. A `wgo` that finds
state written by a newer version refuses to write it and asks you to upgrade.
Builds from before session IDs (state version 2) predate that guard, so state
version 3 stores agent sessions as a JSON array, which those builds cannot
parse: they fail with `failed to parse state file: ... cannot unmarshal array`
and leave the file alone. If you see that error, an old binary is shadowing
the current one; find it with `which -a wgo`.

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
