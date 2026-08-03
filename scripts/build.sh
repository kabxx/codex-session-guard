#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist="$project_root/dist"
mkdir -p "$dist"

cd "$project_root"
go test ./...
go build -trimpath -ldflags '-s -w' -o "$dist/codex-session-guard" ./cmd/csg
printf 'Built: %s\n' "$dist/codex-session-guard"
