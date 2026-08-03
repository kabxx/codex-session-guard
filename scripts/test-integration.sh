#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binary="$project_root/dist/codex-session-guard"
if [ ! -x "$binary" ]; then
    sh "$project_root/scripts/build.sh"
fi

test_root=$(mktemp -d "${TMPDIR:-/tmp}/codex-session-guard-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT HUP INT TERM

bin_dir="$test_root/bin"
guard_home="$test_root/guard-home"
codex_home="$test_root/codex-home"
fake_codex="$test_root/fake-codex"
mkdir -p "$bin_dir" "$guard_home" "$codex_home"
cp "$binary" "$fake_codex"
chmod 755 "$fake_codex"
printf '#!/bin/sh\nexec "%s" "$@"\n' "$fake_codex" > "$bin_dir/codex"
chmod 755 "$bin_dir/codex"
cp "$bin_dir/codex" "$test_root/upstream-codex-before"

export CODEX_SESSION_GUARD_HOME="$guard_home"
export CODEX_HOME="$codex_home"
PATH="$bin_dir:$PATH"
export PATH

"$binary" install --bin-dir "$bin_dir"
cp "$bin_dir/csg" "$bin_dir/codex-recover"
"$binary" install --bin-dir "$bin_dir"
[ ! -e "$bin_dir/codex-recover" ] || { echo 'upgrade left the owned legacy codex-recover command' >&2; exit 1; }
cmp -s "$bin_dir/codex" "$test_root/upstream-codex-before" || { echo 'install modified the upstream codex command' >&2; exit 1; }

[ "$(command -v codex)" = "$bin_dir/codex" ]
[ "$(command -v csg)" = "$bin_dir/csg" ]

"$bin_dir/codex" --fake-exit=0
set +e
"$bin_dir/codex" --fake-exit=7
direct_exit=$?
set -e
[ "$direct_exit" = 7 ] || { echo "direct codex exit was $direct_exit, expected 7" >&2; exit 1; }
direct_count=$(find "$guard_home/runs" -type f -name '*.json' 2>/dev/null | wc -l | tr -d ' ')
[ "$direct_count" = 0 ] || { echo 'direct codex unexpectedly created a monitoring record' >&2; exit 1; }

set +e
"$bin_dir/csg" "$fake_codex" --fake-exit=0
legacy_exit=$?
set -e
[ "$legacy_exit" = 2 ] || { echo 'legacy csg <launcher> syntax was not rejected' >&2; exit 1; }

active_session=99999999-aaaa-4bbb-8ccc-dddddddddddd
"$bin_dir/csg" run "$fake_codex" "--fake-session=$active_session" --fake-sleep-ms=1500 >"$test_root/active.log" 2>&1 &
active_guard_pid=$!
active_output=
active_attempt=0
while [ "$active_attempt" -lt 100 ]; do
    active_output=$("$bin_dir/csg" list)
    case "$active_output" in
        *"$active_session"*) break ;;
    esac
    active_attempt=$((active_attempt + 1))
    sleep 0.05
done
case "$active_output" in
    *'Tracked sessions: 1 active, 0 unknown, 0 crashed'*RUNNING*"$active_session"*) ;;
    *) printf 'csg list did not report the live session as RUNNING:\n%s\n' "$active_output" >&2; exit 1 ;;
esac
wait "$active_guard_pid"
active_output=$("$bin_dir/csg" list)
case "$active_output" in
    *"$active_session"*) echo 'normally exited session remained in csg list' >&2; exit 1 ;;
esac

session_id=bbbbbbbb-cccc-4ddd-8eee-ffffffffffff
set +e
"$bin_dir/csg" run "$fake_codex" "--fake-session=$session_id" --fake-exit=6
exit_code=$?
set -e
[ "$exit_code" = 6 ] || { echo "generic exit code was $exit_code, expected 6" >&2; exit 1; }

list_output=$("$bin_dir/csg" list)
case "$list_output" in
    *"$fake_codex"*) ;;
    *) echo 'recover list missed the original launcher' >&2; exit 1 ;;
esac
case "$list_output" in
    *"$session_id"*) ;;
    *) echo 'recover list missed the session ID' >&2; exit 1 ;;
esac
"$bin_dir/csg" resume "$session_id"

remaining=$(find "$guard_home/runs" -type f -name '*.json' 2>/dev/null | wc -l | tr -d ' ')
[ "$remaining" = 0 ] || { echo 'successful recovery left a run record' >&2; exit 1; }

delete_session=11111111-2222-4333-8444-555555555555
set +e
"$bin_dir/csg" run "$fake_codex" "--fake-session=$delete_session" --fake-exit=8
delete_exit=$?
set -e
[ "$delete_exit" = 8 ] || { echo "delete fixture exit was $delete_exit, expected 8" >&2; exit 1; }
"$bin_dir/csg" delete "$delete_session"

remaining=$(find "$guard_home/runs" -type f -name '*.json' 2>/dev/null | wc -l | tr -d ' ')
[ "$remaining" = 0 ] || { echo 'delete command left a run record' >&2; exit 1; }

"$bin_dir/csg" doctor
"$bin_dir/codex-session-guard" uninstall

[ ! -e "$codex_home/hooks.json" ] || { echo 'tool-created hooks.json remained after uninstall' >&2; exit 1; }
if [ -e "$codex_home/config.toml" ] && grep -q 'Codex Session Guard' "$codex_home/config.toml"; then
    echo 'managed trust block remained after uninstall' >&2
    exit 1
fi
for name in csg codex-session-guard; do
    [ ! -e "$bin_dir/$name" ] || { echo "uninstall left $name" >&2; exit 1; }
done
cmp -s "$bin_dir/codex" "$test_root/upstream-codex-before" || { echo 'uninstall modified the upstream codex command' >&2; exit 1; }

broken_root="$test_root/broken-install"
broken_bin="$broken_root/bin"
broken_guard_home="$broken_root/guard-home"
broken_codex_home="$broken_root/codex-home"
mkdir -p "$broken_bin" "$broken_guard_home" "$broken_codex_home"
printf '%s' '{ invalid json' > "$broken_codex_home/hooks.json"
export CODEX_SESSION_GUARD_HOME="$broken_guard_home"
export CODEX_HOME="$broken_codex_home"

if "$binary" install --bin-dir "$broken_bin"; then
    echo 'invalid hooks.json unexpectedly installed' >&2
    exit 1
fi
for name in csg codex-session-guard; do
    [ ! -e "$broken_bin/$name" ] || { echo "failed install left $name" >&2; exit 1; }
done
[ "$(cat "$broken_codex_home/hooks.json")" = '{ invalid json' ] || {
    echo 'failed install did not restore hooks.json' >&2
    exit 1
}

echo 'Integration test passed.'
