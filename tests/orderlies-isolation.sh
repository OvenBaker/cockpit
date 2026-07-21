#!/usr/bin/env bash
set -euo pipefail

orderlies="${ORDERLIES_SOURCE_ROOT:-$(cd "$(dirname "$0")/../../orderlies-implementation" && pwd)}"
fleet=(orderlies-up orderly-spawn orderly-poller orderlies-status orderlies-down orderlies-heartbeat)
for script in "${fleet[@]}"; do
  test -x "$orderlies/runtime/$script" || test -f "$orderlies/runtime/$script"
done
! rg -n -- '-L cockpit|TMUX="tmux -L cockpit"' "${fleet[@]/#/$orderlies/runtime/}"
! rg -n -- 'pgrep -f orderly-poller' "$orderlies/runtime/orderlies-up"
# The shell source deliberately escapes the JSON literal inside a double-quoted
# argument. This is a static source assertion only; it never calls heartbeat.
rg -q -F '\"home\":\"orderlies\"' "$orderlies/runtime/orderlies-heartbeat"
out=$(ORDERLIES_TMUX_SOCKET=orderlies ORDERLIES_SESSION=orderlies "$orderlies/runtime/orderlies-up" --check-controller-domain)
test "$out" = $'socket=orderlies\nsession=orderlies'
poller_home=$(mktemp -d)
poller=$(HOME="$poller_home" ORDERLIES_TMUX_SOCKET=orderlies ORDERLIES_SESSION=orderlies "$orderlies/runtime/orderly-poller" --check-controller-domain)
test ! -e "$poller_home/.cache"
rm -rf "$poller_home"
[[ "$poller" == *"poller.orderlies.orderlies.pid"* ]] && [[ "$poller" == *"poller.orderlies.orderlies.lock"* ]]
lockhome=$(mktemp -d)
sleep 60 & unrelated=$!
one=''
cleanup_lock_oracle() {
  [[ -n "$one" ]] && kill "$one" 2>/dev/null || true
  kill "$unrelated" 2>/dev/null || true
  wait "$unrelated" 2>/dev/null || true
  rm -rf "$lockhome"
}
trap cleanup_lock_oracle EXIT
mkdir -p "$lockhome/.cache/orderlies"; echo "$unrelated" > "$lockhome/.cache/orderlies/poller.orderlies.orderlies.pid"
ORDERLIES_TEST_LOCK_HOLD=1 HOME="$lockhome" ORDERLIES_TMUX_SOCKET=orderlies ORDERLIES_SESSION=orderlies "$orderlies/runtime/orderly-poller" --test-lock-once >"$lockhome/one" & one=$!
for _ in $(seq 1 100); do
  [[ -s "$lockhome/one" ]] && break
  sleep .01
done
test "$(cat "$lockhome/one")" = acquired
HOME="$lockhome" ORDERLIES_TMUX_SOCKET=orderlies ORDERLIES_SESSION=orderlies "$orderlies/runtime/orderly-poller" --test-lock-once >"$lockhome/two"
wait "$one"; one=''; test "$(cat "$lockhome/one")" = acquired; test "$(cat "$lockhome/two")" = locked
kill -0 "$unrelated"
cleanup_lock_oracle
trap - EXIT
if ORDERLIES_TMUX_SOCKET='bad;socket' "$orderlies/runtime/orderlies-up" --check-controller-domain >/dev/null 2>&1; then
  echo 'unsafe socket accepted' >&2; exit 1
fi
socket="cp-it-orderlies-$RANDOM"
cache=$(mktemp -d)
trap 'tmux -L "$socket" kill-server 2>/dev/null || true; rm -rf "$cache"' EXIT
HOME="$cache" ORDERLIES_TMUX_SOCKET="$socket" ORDERLIES_SESSION=orderlies "$orderlies/runtime/orderlies-up" --bootstrap-only
test "$(tmux -L "$socket" list-windows -t orderlies -F '#{window_name}' | sort | tr '\n' ' ')" = 'aide '
test "$(tmux -L "$socket" list-panes -s -t orderlies -F '#{@orderly}' | grep -cx brief)" = 1
HOME="$cache" ORDERLIES_TMUX_SOCKET="$socket" ORDERLIES_SESSION=orderlies "$orderlies/runtime/orderlies-up" --bootstrap-only
test "$(tmux -L "$socket" list-windows -t orderlies -F '#{window_name}' | sort | tr '\n' ' ')" = 'aide '
test "$(tmux -L "$socket" list-panes -s -t orderlies -F '#{@orderly}' | grep -cx brief)" = 1
