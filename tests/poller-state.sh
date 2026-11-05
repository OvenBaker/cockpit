#!/usr/bin/env bash
# The poller's pane state: every classify branch from fixture transcripts, then a real poller on a scratch
# tmux server, checking that a transcript change is noticed although the poller now re-reads a transcript
# only when its mtime or size moves, and caches transcript paths.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
root=$(mktemp -d)
socket="cp-poller-$RANDOM"
poller=""
trap '[[ -n "$poller" ]] && kill "$poller" 2>/dev/null; tmux -L "$socket" kill-server 2>/dev/null || true; rm -rf "$root"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

export PROJECTS_DIR="$root/projects" CODEX_SESSIONS="$root/codex" SANTA_DB="$root/santa.db"
export COCKPIT_LAYOUT_DB="$root/layout.db"
mkdir -p "$PROJECTS_DIR/-work" "$CODEX_SESSIONS/2026/11/05"
now=$(date +%s)
# write <file> <age-seconds> <lines...>
write() { local f="$1" age="$2"; shift 2; printf '%s\n' "$@" >"$f"; touch -d "@$(( now - age ))" "$f"; }

end_turn='{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}'
tool_use='{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1"}]}}'
streaming='{"type":"assistant","message":{"stop_reason":null,"content":[{"type":"tool_use","id":"t1"}]}}'
tool_result='{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]}}'
prompt='{"type":"user","message":{"content":"hello"}}'
partial='{"type":"assistant","message":{"stop_re'

# --- every classify branch ------------------------------------------------------------------------------
# expect <agent> <want> <age> <lines...>
expect() {
  local agent="$1" want="$2" age="$3" f got; shift 3
  if [[ "$agent" == codex ]]; then f="$CODEX_SESSIONS/2026/11/05/rollout-x-case.jsonl"; else f="$PROJECTS_DIR/-work/case.jsonl"; fi
  write "$f" "$age" "$@"
  got=$(bash -c 'source "$1/lib.sh"; agent_classify "$2" "$3"' _ "$here" "$agent" "$f")
  [[ "$got" == "$want" ]] || fail "$agent age $age, last line ${*: -1}: want $want, got $got"
}
expect claude idle        0  "$prompt" "$end_turn"
expect claude idle      999  "$end_turn"
expect claude working     5  "$tool_use"
expect claude needs-input 30 "$tool_use"
expect claude working     5  "$streaming"
expect claude working     5  "$tool_use" "$tool_result"
expect claude idle       20  "$tool_use" "$tool_result"
expect claude working     5  "$prompt"
expect claude idle       20  "$prompt"
expect claude working     5  "$end_turn" "$partial"
expect claude idle       20  "$end_turn" "$partial"
expect codex  working   999  '{"type":"event_msg","payload":{"type":"task_started"}}' '{"type":"response_item"}'
expect codex  idle        0  '{"type":"event_msg","payload":{"type":"task_started"}}' '{"type":"event_msg","payload":{"type":"task_complete"}}'
expect codex  idle        0  '{"type":"event_msg","payload":{"type":"turn_aborted"}}'
expect codex  working     5  '{"type":"session_meta"}'
expect codex  idle       20  '{"type":"session_meta"}'
got=$(bash -c 'source "$1/lib.sh"; agent_classify claude "$2"' _ "$here" "$root/missing.jsonl")
[[ "$got" == dead ]] || fail "missing transcript: want dead, got $got"

# --- a real poller on a scratch server --------------------------------------------------------------------
sqlite3 "$SANTA_DB" "CREATE TABLE sessions(id TEXT PRIMARY KEY, summary_title TEXT, first_user_text TEXT);
  INSERT INTO sessions VALUES('sess-a', '', 'first	line' || char(10) || 'second'), ('rollout-b', 'Codex title', '');"
write "$PROJECTS_DIR/-work/sess-a.jsonl" 600 "$prompt" "$end_turn"
write "$CODEX_SESSIONS/2026/11/05/rollout-2026-11-05-rollout-b.jsonl" 600 '{"type":"event_msg","payload":{"type":"task_complete"}}'
T="tmux -L $socket"
TMUX='' $T new-session -d -s cockpit -n work 'sleep 600'
TMUX='' $T split-window -t cockpit:0 'sleep 600'
a=$($T list-panes -t cockpit:0 -F '#{pane_id}' | sed -n 1p); b=$($T list-panes -t cockpit:0 -F '#{pane_id}' | sed -n 2p)
$T set -p -t "$a" @agent claude; $T set -p -t "$a" @session_id sess-a; $T set -p -t "$a" @cwd /work
$T set -p -t "$b" @agent codex;  $T set -p -t "$b" @session_id rollout-b; $T set -p -t "$b" @cwd /work
TMUX='' $T new-window -t cockpit -n other 'sleep 600'   # look at another window, so no work pane is "visited"

COCKPIT_TMUX="$T" COCKPIT_SESSION=cockpit COCKPIT_NO_SINGLETON=1 COCKPIT_INTERVAL=0.2 COCKPIT_TITLE_REFRESH=1 \
  bash "$here/cockpit-poller" >"$root/poller.log" 2>&1 &
poller=$!
# wait_for <pane> <option> <value>
wait_for() {
  local i v
  for i in $(seq 1 50); do
    v=$($T show -pqv -t "$1" "$2" 2>/dev/null); [[ "$v" == "$3" ]] && return 0; sleep 0.1
  done
  fail "pane $1 $2: want '$3', got '$v' (poller log: $(cat "$root/poller.log"))"
}
wait_for "$a" @state idle
wait_for "$b" @state idle
wait_for "$a" @badge "○ idle 10m"
wait_for "$a" @label "first line second"        # santa's first prompt, flattened to one line
wait_for "$b" @label "Codex title"
wait_for "$a" @activity_at "$(( now - 600 ))"

# The cached facts must not hide a transcript change: a pending tool call becomes working, then needs-input
# once it has been quiet for COCKPIT_NEEDS_SECS, its result is working again, and the finished turn
# latches just-finished (the latch is a working → idle edge).
printf '%s\n' "$tool_use" >>"$PROJECTS_DIR/-work/sess-a.jsonl"
wait_for "$a" @state working
touch -d "@$(( $(date +%s) - 40 ))" "$PROJECTS_DIR/-work/sess-a.jsonl"
wait_for "$a" @state needs-input
printf '%s\n' "$tool_result" >>"$PROJECTS_DIR/-work/sess-a.jsonl"
wait_for "$a" @state working
printf '%s\n' "$end_turn" >>"$PROJECTS_DIR/-work/sess-a.jsonl"
wait_for "$a" @state just-finished
# A transcript that vanishes is noticed despite the path cache.
rm "$CODEX_SESSIONS/2026/11/05/rollout-2026-11-05-rollout-b.jsonl"
wait_for "$b" @badge "…"
echo "poller-state: ok"
