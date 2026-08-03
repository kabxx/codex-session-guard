#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binary="$project_root/dist/codex-session-guard"
bin_dir=${CSG_BIN_DIR:-"$HOME/.local/bin"}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --bin-dir)
            [ "$#" -ge 2 ] || { echo '--bin-dir requires a path' >&2; exit 2; }
            bin_dir=$2
            shift 2
            ;;
        *)
            echo "unknown option: $1" >&2
            exit 2
            ;;
    esac
done

mkdir -p "$bin_dir"
bin_dir=$(CDPATH= cd -- "$bin_dir" && pwd -P)

if [ ! -x "$binary" ]; then
    sh "$project_root/scripts/build.sh"
fi

"$binary" install --bin-dir "$bin_dir"
PATH="$bin_dir:$PATH" "$bin_dir/csg" doctor

case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *) printf 'Add this directory to PATH: %s\n' "$bin_dir" ;;
esac
printf 'Installed. codex is not monitored; use csg run <launcher> to monitor, then csg list/resume/delete to manage recoveries.\n'
