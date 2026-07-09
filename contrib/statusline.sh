#!/usr/bin/env sh
# contrib/statusline.sh — render the wgo work context for a shell prompt or the
# Claude Code statusline. Reads Claude Code's JSON on stdin (if present) to find
# the working directory, otherwise uses $PWD. Never blocks on the network: PR
# status comes from the ~/.wgo cache, and a miss simply omits the PR segment.
#
# Claude Code:  point your statusLine command at this script.
# Shell prompt: call it from PS1 / precmd (see the README for zsh/fish).
#
# Optional: run a periodic warmer to keep the PR cache fresh, e.g. from cron or
# a shell hook: `wgo -C "$dir" statusline --refresh >/dev/null 2>&1 &`

dir="$PWD"

# Claude Code invokes the statusline with a JSON blob on stdin describing the
# session. When stdin is not a terminal, try to read the current directory from
# it (requires jq; falls back to $PWD without it).
if [ ! -t 0 ]; then
	input="$(cat)"
	if [ -n "$input" ] && command -v jq >/dev/null 2>&1; then
		parsed="$(printf '%s' "$input" | jq -r '.workspace.current_dir // .cwd // empty' 2>/dev/null)"
		[ -n "$parsed" ] && dir="$parsed"
	fi
fi

exec wgo -C "$dir" statusline "$@"
