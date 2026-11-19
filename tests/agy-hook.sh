#!/usr/bin/env bash
# Antigravity (agy) support: the three hook events on a scratch tmux, the owner gate, the {} contract outside
# cockpit, the hooks installer on a temp HOME, candidates/adopt from history.jsonl, and cockpit-spawn with a
# stub `agy`. Nothing here touches the live tmux server, ~/.gemini, or the real agy.
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
root=$(mktemp -d); sock="cp-agy-$RANDOM"
trap 'tmux -L "$sock" kill-server 2>/dev/null; rm -rf "$root"' EXIT
pass=0; fails=0
ok() { if eval "$2"; then echo "  PASS  $1"; pass=$((pass+1)); else echo "  FAIL  $1"; fails=$((fails+1)); fi; }
export HOME="$root/home"; mkdir -p "$HOME"
export AGY_HOME="$root/agy"; mkdir -p "$AGY_HOME/conversations"
export COCKPIT_LAYOUT_DB="$root/layout.db" SANTA_DB="$root/santa.db" PROJECTS_DIR="$root/projects" CODEX_SESSIONS="$root/codex"
export COCKPIT_SOCKET_NAME="$sock" COCKPIT_TMUX="tmux -L $sock" COCKPIT_SESSION=cockpit
T=(tmux -L "$sock")
TMUX='' "${T[@]}" new-session -d -s cockpit -n w -c "$root" 'sleep 600'
TMUX='' "${T[@]}" split-window -t cockpit:0 'sleep 600'
a=$("${T[@]}" list-panes -t cockpit:0 -F '#{pane_id}' | sed -n 1p); b=$("${T[@]}" list-panes -t cockpit:0 -F '#{pane_id}' | sed -n 2p)
spath=$("${T[@]}" display -p '#{socket_path}')
opt() { "${T[@]}" show -pqv -t "$1" "$2" 2>/dev/null; }
hook() {  # pane event json  → stdout of the hook
  TMUX="$spath,1,0" TMUX_PANE="$1" "$here/cockpit-hook" "$2" <<<"$3"
}
CID=11111111-2222-3333-4444-555555555555
payload="{\"conversationId\":\"$CID\",\"cwd\":\"$root\",\"hookEventName\":\"SessionStart\"}"

echo "== hook events"
out=$(hook "$a" agy-session-start "$payload")
ok "session-start prints {}"                  '[[ "$out" == "{}" ]]'
ok "session-start stamps @agent agy"          '[[ "$(opt "$a" @agent)" == agy ]]'
ok "session-start stamps @session_id"         '[[ "$(opt "$a" @session_id)" == "$CID" ]]'
ok "session-start stamps @cwd"                '[[ "$(opt "$a" @cwd)" == "$root" ]]'
ok "session-start stamps idle + @hook_at"     '[[ "$(opt "$a" @hook_state)" == idle && -n "$(opt "$a" @hook_at)" ]]'
out=$(hook "$a" agy-working "$payload")
ok "working prints {} and stamps working"     '[[ "$out" == "{}" && "$(opt "$a" @hook_state)" == working ]]'
out=$(hook "$a" agy-idle '{"conversationId":"'$CID'","terminationReason":"done"}')
ok "idle prints {} and stamps idle"           '[[ "$out" == "{}" && "$(opt "$a" @hook_state)" == idle ]]'
hook "$a" agy-working '{"conversationId":"not-a-uuid"}' >/dev/null
ok "a non-UUID conversationId is never stamped" '[[ "$(opt "$a" @session_id)" == "$CID" ]]'
out=$(hook "$a" agy-working 'garbage{')
ok "malformed stdin still prints {}"          '[[ "$out" == "{}" ]]'

echo "== owner gate"
"${T[@]}" set -p -t "$b" @agent claude
hook "$b" agy-session-start "$payload" >/dev/null
ok "agy event never touches a claude pane"    '[[ "$(opt "$b" @agent)" == claude && -z "$(opt "$b" @session_id)" && -z "$(opt "$b" @hook_state)" ]]'
CS=99999999-2222-3333-4444-555555555555
hook "$a" session-start "{\"session_id\":\"$CS\",\"cwd\":\"$root\"}" >/dev/null
ok "claude event never claims an agy pane"    '[[ "$(opt "$a" @agent)" == agy && "$(opt "$a" @session_id)" == "$CID" ]]'
"${T[@]}" set -p -t "$b" @agent ""
hook "$b" agy-working "{\"conversationId\":\"$CS\",\"cwd\":\"$root\"}" >/dev/null
ok "an unowned pane can be claimed by agy"    '[[ "$(opt "$b" @agent)" == agy && "$(opt "$b" @session_id)" == "$CS" ]]'

echo "== outside cockpit"
out=$(env -u TMUX -u TMUX_PANE "$here/cockpit-hook" agy-working <<<"$payload"; echo "rc=$?")
ok "no tmux: {} and exit 0"                   '[[ "$out" == $'"'"'{}\nrc=0'"'"' ]]'
out=$(TMUX="/tmp/other-server/default,1,0" TMUX_PANE=%0 "$here/cockpit-hook" agy-idle <<<"$payload"; echo "rc=$?")
ok "foreign tmux server: {} and exit 0"       '[[ "$out" == $'"'"'{}\nrc=0'"'"' ]]'
out=$(env -u TMUX -u TMUX_PANE "$here/cockpit-hook" agy-bogus </dev/null; echo "rc=$?")
ok "unknown agy event: {} and exit 0"         '[[ "$out" == $'"'"'{}\nrc=0'"'"' ]]'

echo "== installer (temp HOME)"
H="$HOME/.gemini/config/hooks.json"
ins() { "$here/cockpit-agy-hooks" "$@"; }
ins status >/dev/null; ok "status before install is non-zero" '[[ $? -ne 0 ]]'
ins install >/dev/null
ok "install creates the file with a cockpit key" 'jq -e ".cockpit.SessionStart[0].command == \"~/tools/cockpit/cockpit-hook agy-session-start\" and .cockpit.PreInvocation[0].command == \"~/tools/cockpit/cockpit-hook agy-working\" and .cockpit.Stop[0].command == \"~/tools/cockpit/cockpit-hook agy-idle\" and .cockpit.Stop[0].timeout == 5 and .cockpit.Stop[0].type == \"command\"" "$H" >/dev/null'
ok "status after install is zero"             'ins status >/dev/null'
jq '.mine = {"Stop":[{"type":"command","command":"echo hi"}]}' "$H" > "$H.n" && mv "$H.n" "$H"
ins install >/dev/null
ok "re-install preserves other keys"          'jq -e ".mine.Stop[0].command == \"echo hi\"" "$H" >/dev/null'
ins uninstall >/dev/null
ok "uninstall removes only cockpit"           'jq -e "has(\"cockpit\") | not" "$H" >/dev/null && jq -e ".mine" "$H" >/dev/null'
ok "no temp files left behind"                '[[ "$(ls "$HOME/.gemini/config" | wc -l)" == 1 ]]'
printf 'not json' > "$H"; ins install >/dev/null 2>&1
ok "a non-JSON file is refused, untouched"    '[[ $? -ne 0 && "$(cat "$H")" == "not json" ]]'

echo "== candidates + adopt from history.jsonl"
now=$(date +%s)
A1=aaaaaaaa-0000-0000-0000-000000000001; A2=aaaaaaaa-0000-0000-0000-000000000002
mkdir -p "$root/proj"
touch "$AGY_HOME/conversations/$A1.db" "$AGY_HOME/conversations/$A2.db"
{
  printf '{"display":"/model","timestamp":%d,"workspace":"%s","conversationId":"%s","type":"slash_command"}\n' $(( (now-900)*1000 )) "$root/proj" "$A1"
  printf '{"display":"fix the flaky test","timestamp":%d,"workspace":"%s","conversationId":"%s"}\n' $(( (now-800)*1000 )) "$root/proj" "$A1"
  printf '{"display":"later prompt","timestamp":%d,"workspace":"%s","conversationId":"%s"}\n' $(( (now-700)*1000 )) "$root/proj" "$A1"
  printf '{"display":"/help","timestamp":%d,"workspace":"%s","conversationId":"%s","type":"slash_command"}\n' $(( (now-1200)*1000 )) "$root/proj" "$A2"
} > "$AGY_HOME/history.jsonl"
rows=$(bash -c 'source "$1/lib.sh"; _agy_rows 10' _ "$here")
ok "one row per conversation"                 '[[ "$(wc -l <<<"$rows")" == 2 ]]'
ok "row = latest ts, id, cwd, first real prompt" 'grep -qP "^$((now-700))\tagy\t$A1\t$root/proj\tfix the flaky test$" <<<"$rows"'
ok "slash-only conversation titled by id prefix" 'grep -qP "\t$A2\t$root/proj\t\(agy aaaaaaaa\)$" <<<"$rows"'
ok "adopt: conversation born before the prompt is picked" '[[ "$(bash -c "source $here/lib.sh; cockpit_adopt_agent agy $root/proj $((now-750))")" == "$A1" ]]'
ok "adopt: nothing at/after a later birth"    '[[ -z "$(bash -c "source $here/lib.sh; cockpit_adopt_agent agy $root/proj $((now-100))")" ]]'
ok "adopt: other workspace never matches"     '[[ -z "$(bash -c "source $here/lib.sh; cockpit_adopt_agent agy $root/elsewhere $((now-2000))")" ]]'

echo "== spawn with a stub agy"
mkdir -p "$root/bin"; printf "export PATH=%s:\$PATH\n" "$root/bin" > "$HOME/.profile"; printf '#!/bin/sh\necho "stub agy $*"; sleep 600\n' > "$root/bin/agy"; chmod +x "$root/bin/agy"
mkdir -p "$root/spawn"
pane=$(PATH="$root/bin:$PATH" "$here/cockpit-spawn" --cwd "$root/spawn" --agent agy --model gemini-x --effort max 2>"$root/err")
ok "spawn --agent agy returns a pane"         '[[ "$pane" == %* ]]'
ok "spawn stamps @agent agy and @born"        '[[ "$(opt "$pane" @agent)" == agy && -n "$(opt "$pane" @born)" ]]'
sleep 1
ok "spawn passes --model/--effort"            '"${T[@]}" capture-pane -J -S - -p -t "$pane" | grep -q "stub agy --model gemini-x --effort max"'
PATH="$root/bin:$PATH" "$here/cockpit-spawn" --cwd "$root/spawn" --agent claude --effort max >/dev/null 2>&1
ok "max stays refused for claude"             '[[ $? -eq 2 ]]'
PATH="$root/bin:$PATH" "$here/cockpit-spawn" --cwd "$root/spawn" --agent agy --request-id r1 --initial-prompt-file /x --initial-prompt-sha256 "$(printf 0%.0s {1..64})" --initial-prompt-bytes 3 2>"$root/err" >/dev/null
ok "seeded agy is refused with a clear message" '[[ $? -eq 2 ]] && grep -q "not supported for --agent agy" "$root/err"'

echo "== picker launch"
ok "new-session launch: default"              '[[ "$(source "$here/lib.sh"; source "$here/picker/new-session.sh"; cockpit_new_launch agy "$root" "" auto)" == "exec agy" ]]'
ok "new-session launch: bypass"               '[[ "$(source "$here/lib.sh"; source "$here/picker/new-session.sh"; cockpit_new_launch agy "$root" "" bypass)" == "exec agy --dangerously-skip-permissions" ]]'
ok "ag badge"                                 '[[ "$(source "$here/lib.sh"; cockpit_agent_tag agy)" == ag ]]'

echo "passed=$pass failed=$fails"; [[ $fails -eq 0 ]]
