# Codex hooks for `wgo agent`

Codex can report its session lifecycle to `wgo agent hook`. The hook stores one
entry per Codex `session_id` in the jj workspace containing the session's
`cwd`. `wgo agent status` and `wgo .` then show that session alongside Claude
Code and manually registered agents.

Install a current `wgo` and `jj` on the PATH used by local Codex hooks. Add the
following to `~/.codex/hooks.json` for all projects, or merge it into a
project's `.codex/hooks.json` for that project. Review and trust the hooks in
`/hooks`; project hooks also require a trusted project.

```json
{
  "hooks": {
    "SessionStart": [
      { "matcher": "startup|resume|clear|fork", "hooks": [
        { "type": "command", "command": "wgo agent hook SessionStart --tool codex", "timeout": 6 }
      ] }
    ],
    "UserPromptSubmit": [
      { "hooks": [
        { "type": "command", "command": "wgo agent hook UserPromptSubmit --tool codex", "timeout": 6 }
      ] }
    ],
    "PostToolUse": [
      { "hooks": [
        { "type": "command", "command": "wgo agent hook PostToolUse --tool codex", "timeout": 6 }
      ] }
    ],
    "Stop": [
      { "hooks": [
        { "type": "command", "command": "wgo agent hook Stop --tool codex", "timeout": 6 }
      ] }
    ],
    "Interrupt": [
      { "hooks": [
        { "type": "command", "command": "wgo agent hook Interrupt --tool codex", "timeout": 3 }
      ] }
    ],
    "SessionEnd": [
      { "hooks": [
        { "type": "command", "command": "wgo agent hook SessionEnd --tool codex", "timeout": 3 }
      ] }
    ]
  }
}
```

`SessionStart` sets **waiting**; `UserPromptSubmit` and `PostToolUse` set
**working**; `Stop` and `Interrupt` set **waiting**; `SessionEnd` removes the
entry. The `SessionStart` matcher excludes `compact`, which can occur in the
middle of a working turn. `PostToolUse` is synchronous so it cannot finish
after a later `Stop` and overwrite **waiting** with **working**. wgo throttles
unchanged tool events to at most one state write per minute.

Codex passes `session_id`, `cwd`, and `hook_event_name` as JSON on stdin. wgo
ignores prompt and tool contents. The command line contains no session data.
Codex caps `SessionEnd` and `Interrupt` hooks at three seconds; wgo limits its
state-lock wait to two seconds for Codex `SessionEnd`. A failed or timed-out
`SessionEnd` may leave the entry visible until liveness pruning removes it.
Codex does not fire `SessionEnd` immediately when you switch away from a
conversation.

Local command hooks are unavailable for Codex sessions orchestrated in the
cloud. wgo tracks jj workspaces; a Codex session in a plain Git checkout is
skipped. Codex has no statusline integration through Claude Code's
`statusLine` setting; run `wgo .` or `wgo statusline` as a command instead.

Check the result with:

```bash
wgo agent status
wgo .
```

Codex reads `AGENTS.md` for project instructions. This repository has one, and
`wgo add`, `wgo join`, and `wgo rig` generate a shared `AGENTS.md` alongside
`CLAUDE.md` for multi-repo workspaces and rigs.

See the [Codex hooks documentation](https://developers.openai.com/codex/hooks)
for hook loading, trust, event payloads, and supported environments, and the
[Codex AGENTS.md guide](https://developers.openai.com/codex/agent-configuration/agents-md)
for project instruction discovery.
