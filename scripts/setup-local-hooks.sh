#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'EOF'
Usage:
  scripts/setup-local-hooks.sh install [--worktree|--local]
  scripts/setup-local-hooks.sh uninstall [--worktree|--local]
  scripts/setup-local-hooks.sh status

Installs tracked hook scripts via local Git hook configuration.
By default, install/uninstall uses worktree-local config so older branches and
other worktrees are not affected unless you opt into shared repo-local config.
EOF
}

if [[ $# -eq 0 ]]; then
	set -- install
fi

command_name="$1"
shift

scope_flag="--worktree"
scope_label="worktree"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--worktree)
		scope_flag="--worktree"
		scope_label="worktree"
		;;
	--local)
		scope_flag="--local"
		scope_label="local"
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		echo "error: unknown argument: $1" >&2
		usage >&2
		exit 1
		;;
	esac
	shift
done

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "error: run this command inside a git worktree" >&2
	exit 1
}
cd "$repo_root"

enable_worktree_config() {
	if [[ "$scope_flag" == "--worktree" ]]; then
		git config extensions.worktreeConfig true
	fi
}

set_hook() {
	local hook_name="$1"
	local event_name="$2"
	local command_path="$3"

	git config "$scope_flag" --unset-all "hook.${hook_name}.event" >/dev/null 2>&1 || true
	git config "$scope_flag" --unset-all "hook.${hook_name}.command" >/dev/null 2>&1 || true
	git config "$scope_flag" "hook.${hook_name}.command" "$command_path"
	git config "$scope_flag" --add "hook.${hook_name}.event" "$event_name"
}

unset_hook() {
	local hook_name="$1"

	git config "$scope_flag" --unset-all "hook.${hook_name}.event" >/dev/null 2>&1 || true
	git config "$scope_flag" --unset-all "hook.${hook_name}.command" >/dev/null 2>&1 || true
}

print_status() {
	local found=0

	echo "Configured wgo local CI hooks:"
	while read -r scope key value; do
		found=1
		echo "  [$scope] $key = $value"
	done < <(git config --show-scope --get-regexp '^hook\.wgo-' 2>/dev/null || true)

	if [[ "$found" -eq 0 ]]; then
		echo "  none"
	fi
}

case "$command_name" in
install)
	enable_worktree_config
	set_hook "wgo-pre-commit" "pre-commit" "./scripts/hooks/pre-commit.sh"
	set_hook "wgo-pre-push" "pre-push" "./scripts/hooks/pre-push.sh"
	echo "Installed local CI hooks in ${scope_label} git config."
	echo "  pre-commit -> ./scripts/hooks/pre-commit.sh"
	echo "  pre-push   -> ./scripts/hooks/pre-push.sh"
	echo "These hook scripts are tracked in git, but the hook config itself is not checked in."
	;;
uninstall)
	unset_hook "wgo-pre-commit"
	unset_hook "wgo-pre-push"
	echo "Removed local CI hooks from ${scope_label} git config."
	;;
status)
	print_status
	;;
*)
	echo "error: unknown command: $command_name" >&2
	usage >&2
	exit 1
	;;
esac
