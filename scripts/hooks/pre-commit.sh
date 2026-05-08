#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

staged_files="$(git diff --cached --name-only --diff-filter=ACMR)"
if [[ -z "$staged_files" ]]; then
	exit 0
fi

if ! printf '%s\n' "$staged_files" | grep -Eq '(^|/).*\.go$|^go\.mod$|^go\.sum$'; then
	exit 0
fi

go_files=()
while IFS= read -r file; do
	if [[ -n "$file" ]]; then
		go_files+=("$file")
	fi
done < <(printf '%s\n' "$staged_files" | grep -E '(^|/).*\.go$' || true)

if (( ${#go_files[@]} > 0 )); then
	unformatted="$(gofmt -l "${go_files[@]}")"
	if [[ -n "$unformatted" ]]; then
		echo "pre-commit: gofmt check failed. Run gofmt on:"
		echo "$unformatted"
		exit 1
	fi
fi

go mod tidy
if ! git diff --exit-code -- go.mod go.sum >/dev/null; then
	echo "pre-commit: go mod tidy produced changes. Review and stage go.mod/go.sum."
	git --no-pager diff -- go.mod go.sum || true
	exit 1
fi
