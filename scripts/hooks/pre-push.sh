#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

if ! command -v govulncheck >/dev/null 2>&1; then
	echo "pre-push: govulncheck is required but was not found in PATH."
	echo "Install it with: go install golang.org/x/vuln/cmd/govulncheck@latest"
	exit 1
fi

tmp_bin="$(mktemp "${TMPDIR:-/tmp}/wgo-build.XXXXXX")"
trap 'rm -f "$tmp_bin"' EXIT

echo "pre-push: go build ./cmd/wgo"
go build -o "$tmp_bin" ./cmd/wgo

echo "pre-push: go vet ./..."
go vet ./...

echo "pre-push: go test ./..."
go test ./...

echo "pre-push: govulncheck ./..."
govulncheck ./...
