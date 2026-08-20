#!/usr/bin/env bash
set -euo pipefail

# Real throwaway-tmux proof for the Cockpit-owned launch/adoption contract.
# It never uses the live Cockpit socket or a real provider process.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
root=$(mktemp -d)
socket="cp-it-canonical-$RANDOM-$RANDOM"
tm() { tmux -L "$socket" "$@"; }
cleanup() {
  [[ -n "${poller:-}" ]] && kill "$poller" 2>/dev/null || true
  tm kill-server 2>/dev/null || true
  rm -rf "$root"
}
trap cleanup EXIT

mkdir -p "$root/bin" "$root/sessions" "$root/projects" "$root/work"
printf '%s\n' '#!/usr/bin/env bash' 'exec sleep 600' >"$root/bin/codex"
chmod 700 "$root/bin/codex"
tm new-session -d -s cockpit -n work 'bash -l'

env_common=(PATH="$root/bin:$PATH" COCKPIT_TMUX="tmux -L $socket" COCKPIT_SESSION=cockpit CODEX_SESSIONS="$root/sessions" PROJECTS_DIR="$root/projects" COCKPIT_REMOTE_CONTROL=0)
tm set-option -p -t cockpit:0.0 @state stale
pane=$(env "${env_common[@]}" "$HERE/cockpit-spawn" --cwd "$root/work" --agent codex --name provenance)
[[ "$pane" =~ ^%[0-9]+$ ]]
[[ "$(tm display-message -p -t "$pane" '#{@agent}')" == codex ]]
[[ "$(tm display-message -p -t "$pane" '#{@cwd}')" == "$root/work" ]]
[[ -z "$(tm display-message -p -t "$pane" '#{@session_id}')" ]]
[[ -n "$(tm display-message -p -t "$pane" '#{@born}')" ]]
[[ -z "$(tm display-message -p -t "$pane" '#{@state}')" ]]

id1=11111111-1111-4111-8111-111111111111
sleep 1
jq -n --arg cwd "$root/work" '{type:"session_meta",payload:{cwd:$cwd}}' >"$root/sessions/rollout-test-$id1.jsonl"
printf '%s\n' '{"type":"event","payload":{"type":"task_complete"}}' >>"$root/sessions/rollout-test-$id1.jsonl"
env "${env_common[@]}" COCKPIT_NO_SINGLETON=1 COCKPIT_INTERVAL=1 "$HERE/cockpit-poller" & poller=$!

wait_for() {
  local wantPane="$1" wantID="$2" n=0
  while ((n++ < 50)); do
    [[ "$(tm display-message -p -t "$wantPane" '#{@session_id}')" == "$wantID" && "$(tm display-message -p -t "$wantPane" '#{@state}')" == idle ]] && return 0
    sleep 0.1
  done
  return 1
}
wait_for "$pane" "$id1"

id2=22222222-2222-4222-8222-222222222222
jq -n --arg cwd "$root/work" '{type:"session_meta",payload:{cwd:$cwd}}' >"$root/sessions/rollout-test-$id2.jsonl"
printf '%s\n' '{"type":"event","payload":{"type":"task_complete"}}' >>"$root/sessions/rollout-test-$id2.jsonl"
direct=$(tm split-window -t cockpit:0 -P -F '#{pane_id}' 'sleep 600')
tm set-option -p -t "$direct" @state stale
env "${env_common[@]}" "$HERE/cockpit-adopt" --pane "$direct" --agent codex --session-id "$id2" --cwd "$root/work" >/dev/null
[[ "$(tm display-message -p -t "$direct" '#{@agent}')" == codex ]]
[[ "$(tm display-message -p -t "$direct" '#{@session_id}')" == "$id2" ]]
[[ "$(tm display-message -p -t "$direct" '#{@cwd}')" == "$root/work" ]]
wait_for "$direct" "$id2"
if env "${env_common[@]}" "$HERE/cockpit-adopt" --pane "$direct" --agent codex --session-id "$id1" --cwd "$root/work" >/dev/null 2>&1; then
  echo "cockpit-adopt accepted a conflicting existing session" >&2
  exit 1
fi

id3=33333333-3333-4333-8333-333333333333
projectDir="$root/projects/$(printf '%s' "$root/work" | sed 's#[/.]#-#g')"
mkdir -p "$projectDir"
printf '%s\n' '{"message":{"stop_reason":"end_turn"}}' >"$projectDir/$id3.jsonl"
hookPane=$(tm split-window -t cockpit:0 -P -F '#{pane_id}' 'sleep 600')
socketPath=$(tm display-message -p -t "$hookPane" '#{socket_path}')
printf '{"session_id":"%s","cwd":"%s"}\n' "$id3" "$root/work" | env "${env_common[@]}" COCKPIT_SOCKET_NAME="$socket" TMUX="$socketPath,1,0" TMUX_PANE="$hookPane" "$HERE/cockpit-hook" session-start
[[ "$(tm display-message -p -t "$hookPane" '#{@agent}')" == claude ]]
[[ "$(tm display-message -p -t "$hookPane" '#{@session_id}')" == "$id3" ]]
[[ "$(tm display-message -p -t "$hookPane" '#{@cwd}')" == "$root/work" ]]
wait_for "$hookPane" "$id3"

# Nested providers inherit TMUX_PANE but do not own the parent pane. Claude review hooks inside a Codex pane
# must not replace its provider/session/cwd or stamp its state; conversely a Codex turn-complete notification
# inside a Claude pane must not mark that pane idle.
kill "$poller" 2>/dev/null || true; wait "$poller" 2>/dev/null || true; poller=""
tm set-option -p -t "$direct" @hook_state working
tm set-option -p -t "$direct" @hook_at 111
beforeCodex=$(tm display-message -p -t "$direct" '#{@agent}|#{@session_id}|#{@cwd}|#{@hook_state}|#{@hook_at}')
for event in session-start working heartbeat idle stop needs-input session-end notification; do
  printf '{"session_id":"%s","cwd":"%s","notification_type":"permission_prompt","prompt":"nested review"}\n' "$id3" "$root/work" |
    env "${env_common[@]}" COCKPIT_SOCKET_NAME="$socket" TMUX="$socketPath,1,0" TMUX_PANE="$direct" "$HERE/cockpit-hook" "$event"
done
afterCodex=$(tm display-message -p -t "$direct" '#{@agent}|#{@session_id}|#{@cwd}|#{@hook_state}|#{@hook_at}')
[[ "$afterCodex" == "$beforeCodex" ]]

tm set-option -p -t "$hookPane" @hook_state working
tm set-option -p -t "$hookPane" @hook_at 222
beforeClaude=$(tm display-message -p -t "$hookPane" '#{@agent}|#{@session_id}|#{@cwd}|#{@hook_state}|#{@hook_at}')
env "${env_common[@]}" COCKPIT_SOCKET_NAME="$socket" TMUX="$socketPath,1,0" TMUX_PANE="$hookPane" \
  "$HERE/cockpit-hook" codex-notify '{"type":"agent-turn-complete"}'
afterClaude=$(tm display-message -p -t "$hookPane" '#{@agent}|#{@session_id}|#{@cwd}|#{@hook_state}|#{@hook_at}')
[[ "$afterClaude" == "$beforeClaude" ]]

# The explicit repair boundary can restore metadata only when the pane's foreground executable proves the
# asserted provider. The ordinary adopt path still refuses the same conflicting binding.
cp /bin/sleep "$root/bin/codex"
repairPane=$(tm split-window -t cockpit:0 -P -F '#{pane_id}' "$root/bin/codex 600")
tm set-option -p -t "$repairPane" @agent claude
tm set-option -p -t "$repairPane" @session_id "$id3"
tm set-option -p -t "$repairPane" @cwd "$root"
if env "${env_common[@]}" "$HERE/cockpit-adopt" --pane "$repairPane" --agent codex --session-id "$id2" --cwd "$root/work" >/dev/null 2>&1; then
  echo "cockpit-adopt repaired a conflicting binding without the repair flag" >&2
  exit 1
fi
env "${env_common[@]}" "$HERE/cockpit-adopt" --pane "$repairPane" --agent codex --session-id "$id2" --cwd "$root/work" \
  --repair-conflicting-binding >/dev/null
[[ "$(tm display-message -p -t "$repairPane" '#{@agent}|#{@session_id}|#{@cwd}|#{@hook_state}')" == "codex|$id2|$root/work|" ]]
