#!/bin/sh
set -eu

bin_dir=${CSG_BIN_DIR:-"$HOME/.local/bin"}
manager="$bin_dir/codex-session-guard"
[ -x "$manager" ] || { printf 'Not installed: %s\n' "$manager" >&2; exit 1; }

"$manager" uninstall
printf 'Uninstalled. PATH entries, recovery records, and backups were preserved.\n'
