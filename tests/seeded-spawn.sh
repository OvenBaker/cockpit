#!/usr/bin/env bash
# Seeded-spawn contract test (contract 1). Real tmux on an isolated throwaway socket (strictly more faithful
# than a fake tmux), plus a faithful fake `claude` that mirrors the provider lifecycle the contract binds to:
# it records the EXACT argv it was exec'd with and fires the real cockpit-hook with real hook-shaped JSON
# (session-start / UserPromptSubmit(working) / session-end) exactly as the installed provider does.
#
# Evidence map (builder brief items 1-10):
#   1  exact multi-line UTF-8 prompt is Claude's first positional prompt, delivered once   → t_atomic_prompt
#   2  no send-keys/paste route exists in the seeded path                                  → t_no_sendkeys
#   3  pane/request metadata is pending before any provider hook event                     → t_pending_before_hooks
#   4  session-start + session id adoption alone stays pending                             → t_start_not_acceptance
#   5  matching UserPromptSubmit accepts (metadata only); mismatch fails; no later repair  → t_accept, t_mismatch
#   6  same request+material replays: after success, while pending, both crash windows     → t_replay_*
#   7  changed material under a reused request conflicts before any spawn                  → t_drift
#   8  provider exit before acceptance fails with a bounded content-free code              → t_exit_before_accept
#   9  non-seeded claude/codex/shell spawns and state JSON stay compatible                 → t_unseeded_compat
#  10  invalid/partial flags and hostile prompt files fail before side effects             → t_validation
#  14  interaction profile (RELEASE-AMEND-001): agent launches carry EXACTLY the agent argv/env delta;
#      human/default launches carry none of it (byte-identical argv); the profile is bound into request
#      identity (drift conflicts, zero extra panes/starts); typed validation fails closed  → t_profile
set -euo pipefail
REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
root=$(mktemp -d)
socket="cpseed-$RANDOM"
export COCKPIT_SESSION=cockpit
export COCKPIT_TMUX="tmux -L $socket"
export XDG_STATE_HOME="$root/state"
export COCKPIT_SEED_DIR="$root/state/cockpit/seeds"
export COCKPIT_CLAUDE_BIN="$root/bin/fake-claude"
trap 'tmux -L "$socket" kill-server 2>/dev/null || true; rm -rf "$root"' EXIT
mkdir -p "$root/bin" "$root/out" "$root/cwd" "$COCKPIT_SEED_DIR"

# Faithful fake claude: append exec count, record exact argv NUL-separated, then fire the real cockpit-hook
# like the provider does. Modes (read per-run from $root/out/mode): hold (no hooks), start-only, full,
# full-then-more, mismatch, mismatch-then-correct, exit-early.
cat > "$root/bin/fake-claude" <<FAKE
#!/usr/bin/env bash
set -u
OUT="$root/out"; REPO="$REPO"; SOCKET="$socket"
hook() { printf '%s' "\$2" | COCKPIT_SOCKET_NAME="\$SOCKET" XDG_STATE_HOME="$root/state" COCKPIT_SEED_DIR="$root/state/cockpit/seeds" "\$REPO/cockpit-hook" "\$1"; }
echo start >> "\$OUT/starts"
: > "\$OUT/argv"; for a in "\$@"; do printf '%s\0' "\$a" >> "\$OUT/argv"; done
printf '%s' "\${AGENT_DRIVEN:-}" > "\$OUT/agent_driven"
mode=\$(cat "\$OUT/mode" 2>/dev/null || echo hold)
prompt="\${@: -1}"
sid="11111111-2222-3333-4444-555555555555"
json() { jq -cn --arg s "\$sid" --arg p "\$1" --arg c "\$PWD" '{session_id:\$s,cwd:\$c,prompt:\$p}'; }
case "\$mode" in
  hold) : ;;
  start-only) hook session-start "\$(json "")";;
  full) hook session-start "\$(json "")"; hook working "\$(json "\$prompt")";;
  full-then-more) hook session-start "\$(json "")"; hook working "\$(json "\$prompt")"; hook working "\$(json "a totally different later turn")";;
  mismatch) hook session-start "\$(json "")"; hook working "\$(json "tampered \$prompt")";;
  mismatch-then-correct) hook session-start "\$(json "")"; hook working "\$(json "tampered \$prompt")"; hook working "\$(json "\$prompt")";;
  exit-early) hook session-start "\$(json "")"; hook session-end "\$(json "")"; exit 3;;
esac
exec sleep 300
FAKE
chmod +x "$root/bin/fake-claude"

TMUX='' tmux -L "$socket" new-session -d -s cockpit -n work 'sleep 600'

# ── helpers ────────────────────────────────────────────────────────────────────────────────────────────────
PROMPT_FILE="$root/prompt.txt"
printf 'Resume brief development for briefs/demo/brief.md.\n\nKeep the canonical id `demo` — émojis 🚀 and tabs\ttoo.\nAsk one bounded question at a time.\n' > "$PROMPT_FILE"
SHA=$(sha256sum < "$PROMPT_FILE" | cut -d' ' -f1)
BYTES=$(wc -c < "$PROMPT_FILE")
spawn() { "$REPO/cockpit-spawn" --cwd "$root/cwd" --workspace seedws --name seed-demo \
  --request-id "$1" --initial-prompt-file "${2:-$PROMPT_FILE}" \
  --initial-prompt-sha256 "${3:-$SHA}" --initial-prompt-bytes "${4:-$BYTES}" "${@:5}"; }
record_of() { XDG_STATE_HOME="$root/state" COCKPIT_SEED_DIR="$root/state/cockpit/seeds" \
  bash -c "source '$REPO/lib.sh'; cockpit_seed_record_path '$1'"; }
field() { jq -r ".$2" "$(record_of "$1")"; }
pane_count() { tmux -L "$socket" list-panes -s -t cockpit -F '#{pane_id}' | wc -l; }
starts() { wc -l < "$root/out/starts" 2>/dev/null || echo 0; }
wait_for() { local i; for i in $(seq 1 50); do eval "$1" && return 0; sleep 0.2; done; echo "timeout: $1" >&2; return 1; }
state_json() { "$REPO/cockpit-state"; }

# ── 3: pending before ANY provider hook event ──────────────────────────────────────────────────────────────
echo hold > "$root/out/mode"; : > "$root/out/starts"
pane1=$(spawn req-pending)
[[ "$pane1" == %* ]]
wait_for '[[ $(starts) -eq 1 ]]'
[[ "$(field req-pending status)" == pending ]]
[[ "$(tmux -L "$socket" display -p -t "$pane1" '#{@seed_status}')" == pending ]]
state_json | jq -e --arg p "$pane1" '.panes[] | select(.pane_id==$p) | .initial_turn.status=="pending"' >/dev/null
echo "ok 3 pending-before-hooks"

# ── 6b: replay while pending → same pane, zero additional starts ───────────────────────────────────────────
again=$(spawn req-pending)
[[ "$again" == "$pane1" ]]
[[ $(starts) -eq 1 ]]
echo "ok 6b replay-while-pending"

# ── 7: material drift under a reused request id conflicts BEFORE any spawn ─────────────────────────────────
n=$(pane_count)
printf 'other prompt\n' > "$root/drift.txt"
dsha=$(sha256sum < "$root/drift.txt" | cut -d' ' -f1); dbytes=$(wc -c < "$root/drift.txt")
set +e
"$REPO/cockpit-spawn" --cwd "$root/cwd" --workspace seedws --name seed-demo --request-id req-pending \
  --initial-prompt-file "$root/drift.txt" --initial-prompt-sha256 "$dsha" --initial-prompt-bytes "$dbytes" >/dev/null 2>&1; rc=$?
set -e
[[ $rc -eq 5 ]]
drift_case() { # base flags first, override LAST (last-wins parsing) → genuine material drift
  set +e; "$REPO/cockpit-spawn" --cwd "$root/cwd" --workspace seedws --name seed-demo \
    --request-id req-pending --initial-prompt-file "$PROMPT_FILE" --initial-prompt-sha256 "$SHA" \
    --initial-prompt-bytes "$BYTES" "$@" >/dev/null 2>&1; local rc=$?; set -e
  [[ $rc -eq 5 ]] || { echo "drift case '$*' expected conflict 5, got $rc" >&2; return 1; }
}
drift_case --cwd "$root"
drift_case --workspace otherws
drift_case --name other-name
[[ $(pane_count) -eq $n && $(starts) -eq 1 ]]
echo "ok 7 drift-conflicts-before-spawn"

# ── 1+4+5: exact prompt delivered once; session-start alone stays pending; acceptance on exact match ───────
echo start-only > "$root/out/mode"; : > "$root/out/starts"
pane2=$(spawn req-accept)
wait_for '[[ $(starts) -eq 1 ]]'
# argv: last element is the EXACT prompt bytes, preceded by --, with remote-control preserved
python3 - "$root/out/argv" "$PROMPT_FILE" <<'PY'
import sys
argv = open(sys.argv[1],'rb').read().split(b'\0')[:-1]
expected = open(sys.argv[2],'rb').read()
assert argv[-1] == expected, (argv[-1], expected)
assert argv[-2] == b'--'
assert b'--remote-control' in argv
PY
sleep 0.5
[[ "$(field req-accept status)" == pending ]]   # session-start + adopted session id is NOT acceptance
[[ "$(tmux -L "$socket" display -p -t "$pane2" '#{@session_id}')" == 1111* ]]
echo "ok 4 session-start-not-acceptance; ok 1 exact-atomic-prompt"

echo full-then-more > "$root/out/mode"; : > "$root/out/starts"
pane3=$(spawn req-accept2)
wait_for '[[ "$(field req-accept2 status)" == accepted ]]'
[[ "$(field req-accept2 sha256)" == "$SHA" ]]
[[ "$(field req-accept2 bytes)" -eq "$BYTES" ]]
[[ "$(field req-accept2 session_id)" == 1111* ]]
[[ -n "$(field req-accept2 accepted_at)" ]]
state_json | jq -e --arg p "$pane3" '.panes[] | select(.pane_id==$p) | .initial_turn.status=="accepted" and .initial_turn.contract==1' >/dev/null
# later user turns cannot rewrite the accepted result; no prompt content anywhere in record or export
grep -q 'émojis' "$(record_of req-accept2)" && exit 1
state_json | grep -q 'émojis' && exit 1
echo "ok 5a accepted-with-safe-metadata-only"

echo mismatch-then-correct > "$root/out/mode"; : > "$root/out/starts"
spawn req-mismatch >/dev/null
wait_for '[[ "$(field req-mismatch status)" == failed ]]'
[[ "$(field req-mismatch error_code)" == first-prompt-mismatch ]]
sleep 0.5  # the later CORRECT prompt arrives after failure…
[[ "$(field req-mismatch status)" == failed ]]   # …and cannot repair it
echo "ok 5b mismatch-fails-terminally"

# ── 6a: replay after success → same pane, zero starts/submissions ──────────────────────────────────────────
: > "$root/out/starts"
[[ "$(spawn req-accept2)" == "$pane3" ]]
[[ $(starts) -eq 0 ]]
echo "ok 6a replay-after-success"

# ── 6c: crash window A (reserved, no pane anywhere) reconciles by binding → ONE recovery launch ────────────
echo hold > "$root/out/mode"; : > "$root/out/starts"
XDG_STATE_HOME="$root/state" COCKPIT_SEED_DIR="$root/state/cockpit/seeds" bash -c "
  source '$REPO/lib.sh'
  rec=\$(cockpit_seed_record_path req-crash-a)
  cockpit_seed_write \"\$rec\" \"\$(jq -cn --arg s '$SHA' --arg b '$BYTES' --arg c \"\$(realpath '$root/cwd')\" \
    '{contract:1,request_id:\"req-crash-a\",sha256:\$s,bytes:(\$b|tonumber),provider:\"claude\",cwd:\$c,workspace:\"seedws\",name:\"seed-demo\",status:\"pending\",pane_id:\"\",session_id:\"\",accepted_at:\"\",error_code:\"\",created_at:\"0\"}')\"
"
paneA=$(spawn req-crash-a)
[[ "$paneA" == %* ]]
wait_for '[[ $(starts) -eq 1 ]]'
[[ "$(field req-crash-a pane_id)" == "$paneA" ]]
[[ "$(spawn req-crash-a)" == "$paneA" ]]   # and the recovered pane replays stably
[[ $(starts) -eq 1 ]]
echo "ok 6c crash-window-A-single-recovery"

# ── 6d+8: crash window B (bound pane died before acceptance) → terminal bounded failure, no respawn ────────
tmux -L "$socket" kill-pane -t "$paneA"
n=$(pane_count); : > "$root/out/starts"
set +e; "$REPO/cockpit-spawn" --cwd "$root/cwd" --workspace seedws --name seed-demo --request-id req-crash-a \
  --initial-prompt-file "$PROMPT_FILE" --initial-prompt-sha256 "$SHA" --initial-prompt-bytes "$BYTES" >/dev/null 2>&1; rc=$?; set -e
[[ $rc -eq 4 ]]
[[ "$(field req-crash-a status)" == failed ]]
[[ "$(field req-crash-a error_code)" == provider-exited-before-acceptance ]]
[[ $(pane_count) -eq $n && $(starts) -eq 0 ]]
echo "ok 6d dead-pane-fails-terminally-no-respawn"

# ── 8: provider exit (SessionEnd) before acceptance → bounded content-free failure ─────────────────────────
echo exit-early > "$root/out/mode"; : > "$root/out/starts"
spawn req-exit >/dev/null
wait_for '[[ "$(field req-exit status)" == failed ]]'
[[ "$(field req-exit error_code)" == provider-exited-before-acceptance ]]
grep -q 'Resume brief' "$(record_of req-exit)" && exit 1
echo "ok 8 provider-exit-fails-bounded"

# ── 2: no send-keys / paste route anywhere in the seeded path (code, not comments) ─────────────────────────
no_sendkeys() {
  if grep -v '^[[:space:]]*#' "$1" | grep -nE 'send-keys|paste-buffer|load-buffer|pipe-pane'; then
    echo "send-keys/paste route found in $1" >&2; exit 1
  fi
}
no_sendkeys "$REPO/cockpit-seed-exec"
no_sendkeys "$REPO/cockpit-spawn"
if sed -n '/seeded first-turn requests/,/session selection/p' "$REPO/lib.sh" \
    | grep -v '^[[:space:]]*#' | grep -nE 'send-keys|paste-buffer|load-buffer|pipe-pane'; then
  echo "send-keys/paste route found in lib.sh seed helpers" >&2; exit 1
fi
echo "ok 2 no-sendkeys-route"

# ── 9: non-seeded spawns + state JSON stay compatible (explicit initial_turn none) ─────────────────────────
plain=$("$REPO/cockpit-spawn" --cwd "$root/cwd" --workspace plainws --name plain-demo)
[[ "$plain" == %* ]]
shellpane=$("$REPO/cockpit-spawn" --cwd "$root/cwd" --workspace plainws --agent shell)
[[ "$shellpane" == %* ]]
state_json | jq -e --arg p "$plain" '.panes[] | select(.pane_id==$p) | .initial_turn.status=="none" and .initial_turn.contract==1' >/dev/null
state_json | jq -e --arg p "$shellpane" '.panes[] | select(.pane_id==$p) | .initial_turn.status=="none"' >/dev/null
state_json | jq -e '.running==true and (.workspaces|type=="array") and (.panes[0]|has("pane_id") and has("session_id") and has("agent") and has("state"))' >/dev/null
echo "ok 9 unseeded-compat"

# ── 10: hostile/partial inputs fail BEFORE side effects (no record, no pane, no start) ─────────────────────
n=$(pane_count); : > "$root/out/starts"
refused() { set +e; "$REPO/cockpit-spawn" "$@" >/dev/null 2>&1; local rc=$?; set -e; [[ $rc -eq 2 ]]; }
base=(--cwd "$root/cwd" --workspace seedws --name seed-demo)
refused "${base[@]}" --request-id req-v1 --initial-prompt-file "$PROMPT_FILE" --initial-prompt-sha256 "$SHA"   # partial set
refused "${base[@]}" --agent codex --request-id req-v2 --initial-prompt-file "$PROMPT_FILE" --initial-prompt-sha256 "$SHA" --initial-prompt-bytes "$BYTES"
refused "${base[@]}" --request-id '../evil' --initial-prompt-file "$PROMPT_FILE" --initial-prompt-sha256 "$SHA" --initial-prompt-bytes "$BYTES"
refused "${base[@]}" --request-id req-v3 --initial-prompt-file relative.txt --initial-prompt-sha256 "$SHA" --initial-prompt-bytes "$BYTES"
ln -s "$PROMPT_FILE" "$root/link.txt"
refused "${base[@]}" --request-id req-v4 --initial-prompt-file "$root/link.txt" --initial-prompt-sha256 "$SHA" --initial-prompt-bytes "$BYTES"
printf 'bad \xff utf8\n' > "$root/bad-utf8.txt"
refused "${base[@]}" --request-id req-v5 --initial-prompt-file "$root/bad-utf8.txt" \
  --initial-prompt-sha256 "$(sha256sum < "$root/bad-utf8.txt" | cut -d' ' -f1)" --initial-prompt-bytes "$(wc -c < "$root/bad-utf8.txt")"
printf 'ctrl \x01 char\n' > "$root/ctrl.txt"
refused "${base[@]}" --request-id req-v6 --initial-prompt-file "$root/ctrl.txt" \
  --initial-prompt-sha256 "$(sha256sum < "$root/ctrl.txt" | cut -d' ' -f1)" --initial-prompt-bytes "$(wc -c < "$root/ctrl.txt")"
head -c 70000 /dev/zero | tr '\0' 'a' > "$root/big.txt"
refused "${base[@]}" --request-id req-v7 --initial-prompt-file "$root/big.txt" \
  --initial-prompt-sha256 "$(sha256sum < "$root/big.txt" | cut -d' ' -f1)" --initial-prompt-bytes "$(wc -c < "$root/big.txt")"
refused "${base[@]}" --request-id req-v8 --initial-prompt-file "$PROMPT_FILE" --initial-prompt-sha256 "$SHA" --initial-prompt-bytes "$((BYTES+1))"
refused "${base[@]}" --request-id req-v9 --initial-prompt-file "$PROMPT_FILE" \
  --initial-prompt-sha256 "0000000000000000000000000000000000000000000000000000000000000000" --initial-prompt-bytes "$BYTES"
for r in req-v1 req-v2 req-v3 req-v4 req-v5 req-v6 req-v7 req-v8 req-v9; do
  [[ ! -f "$(record_of "$r")" ]] || { echo "record leaked for $r" >&2; exit 1; }
done
[[ $(pane_count) -eq $n && $(starts) -eq 0 ]]
echo "ok 10 validation-before-side-effects"

# ── 11: symlink victim traces — a planted state leaf must NEVER redirect a write onto an external file ─────
victim="$root/victim.txt"; printf 'VICTIM-CONTENT\n' > "$victim"
vsha=$(sha256sum < "$victim" | cut -d' ' -f1)
seed_path() { XDG_STATE_HOME="$root/state" COCKPIT_SEED_DIR="$root/state/cockpit/seeds" \
  bash -c "source '$REPO/lib.sh'; cockpit_seed_${2}_path '$1'"; }
n=$(pane_count); : > "$root/out/starts"
# (a) the reviewer's exact trace: pre-planted <request-hash>.prompt symlink → victim
ln -s "$victim" "$(seed_path req-sym-staging staging)"
set +e; spawn req-sym-staging >/dev/null 2>&1; rc=$?; set -e
[[ $rc -eq 2 ]]
[[ "$(sha256sum < "$victim" | cut -d' ' -f1)" == "$vsha" ]]   # victim byte-identical — never overwritten
# (b) planted record symlink
ln -s "$victim" "$(seed_path req-sym-record record)"
set +e; spawn req-sym-record >/dev/null 2>&1; rc=$?; set -e
[[ $rc -eq 2 ]]
[[ "$(sha256sum < "$victim" | cut -d' ' -f1)" == "$vsha" ]]
# (c) planted spawn-lock symlink
( set -C; : > /dev/null ) 2>/dev/null
ln -s "$victim" "$(seed_path req-sym-lock record).spawn.lock"
set +e; spawn req-sym-lock >/dev/null 2>&1; rc=$?; set -e
[[ $rc -eq 2 ]]
[[ "$(sha256sum < "$victim" | cut -d' ' -f1)" == "$vsha" ]]
# (d) the seed state DIR itself is a symlink → fail closed before anything
ln -s "$root/elsewhere" "$root/dirlink"; mkdir -p "$root/elsewhere"
set +e; COCKPIT_SEED_DIR="$root/dirlink" spawn req-sym-dir >/dev/null 2>&1; rc=$?; set -e
[[ $rc -eq 2 ]]
[[ $(pane_count) -eq $n && $(starts) -eq 0 ]]
echo "ok 11 symlink-victim-never-written"

# ── 12: concurrent same-request barrier — one canonical pane identity, one provider start ──────────────────
echo hold > "$root/out/mode"; : > "$root/out/starts"; : > "$root/out/conc"
n=$(pane_count)
for i in $(seq 1 30); do ( spawn req-conc >> "$root/out/conc" 2>/dev/null ) & done
wait
[[ $(wc -l < "$root/out/conc") -eq 30 ]]                       # every caller succeeded with an identity
[[ $(sort -u "$root/out/conc" | wc -l) -eq 1 ]]                # …and it is the SAME pane for all 30
grep -qx "$(sort -u "$root/out/conc")" <(tmux -L "$socket" list-panes -s -t cockpit -F '#{pane_id}')
[[ $(pane_count) -eq $((n+1)) ]]                               # exactly one new pane
wait_for '[[ $(starts) -eq 1 ]]'                               # exactly one provider start
[[ "$(field req-conc pane_id)" == "$(sort -u "$root/out/conc")" ]]
echo "ok 12 concurrent-single-identity"

# ── 13: ambiguous crash window — pane created and launcher ran, but the SPAWNER died before its own binding ─
echo hold > "$root/out/mode"; : > "$root/out/starts"
XDG_STATE_HOME="$root/state" COCKPIT_SEED_DIR="$root/state/cockpit/seeds" bash -c "
  source '$REPO/lib.sh'
  rec=\$(cockpit_seed_record_path req-amb)
  cockpit_seed_write \"\$rec\" \"\$(jq -cn --arg s '$SHA' --arg b '$BYTES' --arg c \"\$(realpath '$root/cwd')\" \
    '{contract:1,request_id:\"req-amb\",sha256:\$s,bytes:(\$b|tonumber),provider:\"claude\",cwd:\$c,workspace:\"seedws\",name:\"seed-demo\",status:\"pending\",pane_id:\"\",session_id:\"\",accepted_at:\"\",error_code:\"\",created_at:\"0\"}')\"
  cp '$PROMPT_FILE' \"\$(cockpit_seed_staging_path req-amb)\"; chmod 600 \"\$(cockpit_seed_staging_path req-amb)\"
"
ambrec=$(seed_path req-amb record); ambstage=$(seed_path req-amb staging)
# Simulate the spawner crashing right after split-window: the pane + launcher exist, the spawner never set
# pane options or updated the record. The IN-PANE launcher must bind the pane to the request by itself.
ambpane=$(tmux -L "$socket" split-window -t cockpit -P -F '#{pane_id}' \
  "bash -lc 'cd $(printf %q "$root/cwd") && exec $(printf %q "$REPO/cockpit-seed-exec") $(printf %q "$ambrec") $(printf %q "$ambstage") seed-demo $(printf %q "$root/cwd") $(printf %q "$root/bin/fake-claude")'")
wait_for '[[ "$(field req-amb pane_id)" == "$ambpane" ]]'      # launcher-side binding closed the window
wait_for '[[ $(starts) -eq 1 ]]'
n=$(pane_count)
[[ "$(spawn req-amb)" == "$ambpane" ]]                          # replay reconciles onto THAT pane…
[[ $(pane_count) -eq $n && $(starts) -eq 1 ]]                   # …zero extra panes/starts
[[ "$(field req-amb status)" == pending ]]
echo "ok 13 ambiguous-window-binds-and-reconciles"

# ── 14: interaction profile — exact agent-only argv/env delta, human/default byte-identical, identity-bound ─
# (a) agent profile: EXACT argv delta + AGENT_DRIVEN=1, and the unchanged prompt/acceptance contract on top.
echo full > "$root/out/mode"; : > "$root/out/starts"
paneAg=$(spawn req-prof-agent "$PROMPT_FILE" "$SHA" "$BYTES" --interaction-profile agent)
[[ "$paneAg" == %* ]]
wait_for '[[ "$(field req-prof-agent status)" == accepted ]]'   # acceptance contract unaffected by profile
[[ "$(field req-prof-agent profile)" == agent ]]
[[ "$(cat "$root/out/agent_driven")" == 1 ]]
python3 - "$root/out/argv" "$PROMPT_FILE" <<'PY'
import sys
argv = open(sys.argv[1],'rb').read().split(b'\0')[:-1]
prompt = open(sys.argv[2],'rb').read()
assert argv[:2] == [b'--remote-control', b'seed-demo'], argv[:2]
assert argv[2:8] == [b'--permission-mode', b'bypassPermissions',
                     b'--disallowedTools', b'AskUserQuestion', b'EnterPlanMode',
                     b'--append-system-prompt'], argv[2:8]
text = argv[8]
assert b'another AI agent' in text and b'BLOCKED:' in text, text
assert argv[9:] == [b'--', prompt], argv[9:]
assert len(argv) == 11, argv          # exactly the agent delta — nothing more, nothing less
PY
# (b) same request + same profile replays onto the same pane with zero extra starts
: > "$root/out/starts"
[[ "$(spawn req-prof-agent "$PROMPT_FILE" "$SHA" "$BYTES" --interaction-profile agent)" == "$paneAg" ]]
[[ $(starts) -eq 0 ]]
# (c) profile drift under the reused request id conflicts BEFORE any spawn — zero extra panes/starts
n=$(pane_count)
set +e; spawn req-prof-agent >/dev/null 2>&1; rc=$?; set -e                                   # omitted → human
[[ $rc -eq 5 ]]
set +e; spawn req-prof-agent "$PROMPT_FILE" "$SHA" "$BYTES" --interaction-profile human >/dev/null 2>&1; rc=$?; set -e
[[ $rc -eq 5 ]]
[[ $(pane_count) -eq $n && $(starts) -eq 0 ]]
# (d) explicit human: argv byte-identical to the pre-profile launch, no AGENT_DRIVEN, profile bound as human
echo hold > "$root/out/mode"; : > "$root/out/starts"
paneH=$(spawn req-prof-human "$PROMPT_FILE" "$SHA" "$BYTES" --interaction-profile human)
wait_for '[[ $(starts) -eq 1 ]]'
[[ "$(field req-prof-human profile)" == human ]]
[[ -z "$(cat "$root/out/agent_driven")" ]]
python3 - "$root/out/argv" "$PROMPT_FILE" <<'PY'
import sys
argv = open(sys.argv[1],'rb').read().split(b'\0')[:-1]
prompt = open(sys.argv[2],'rb').read()
assert argv == [b'--remote-control', b'seed-demo', b'--', prompt], argv
PY
# (e) omitted profile: defaults to human — same byte-identical argv, and the record says so
: > "$root/out/starts"
spawn req-prof-default >/dev/null
wait_for '[[ $(starts) -eq 1 ]]'
[[ "$(field req-prof-default profile)" == human ]]
[[ -z "$(cat "$root/out/agent_driven")" ]]
python3 - "$root/out/argv" "$PROMPT_FILE" <<'PY'
import sys
argv = open(sys.argv[1],'rb').read().split(b'\0')[:-1]
prompt = open(sys.argv[2],'rb').read()
assert argv == [b'--remote-control', b'seed-demo', b'--', prompt], argv
PY
# …and a human-by-default request replayed as agent is drift too (both directions conflict)
n=$(pane_count); : > "$root/out/starts"
set +e; spawn req-prof-default "$PROMPT_FILE" "$SHA" "$BYTES" --interaction-profile agent >/dev/null 2>&1; rc=$?; set -e
[[ $rc -eq 5 ]]
[[ $(pane_count) -eq $n && $(starts) -eq 0 ]]
# (f) typed validation fails closed before side effects; the flag is seeded-contract-only
n=$(pane_count)
refused "${base[@]}" --request-id req-prof-v1 --initial-prompt-file "$PROMPT_FILE" \
  --initial-prompt-sha256 "$SHA" --initial-prompt-bytes "$BYTES" --interaction-profile bogus
refused "${base[@]}" --request-id req-prof-v2 --initial-prompt-file "$PROMPT_FILE" \
  --initial-prompt-sha256 "$SHA" --initial-prompt-bytes "$BYTES" --interaction-profile ""
refused "${base[@]}" --interaction-profile agent            # no seeded contract → refused, not ignored
for r in req-prof-v1 req-prof-v2; do
  [[ ! -f "$(record_of "$r")" ]] || { echo "record leaked for $r" >&2; exit 1; }
done
[[ $(pane_count) -eq $n && $(starts) -eq 0 ]]
echo "ok 14 interaction-profile-selective-and-identity-bound"

echo "ALL SEEDED-SPAWN CHECKS PASSED"
