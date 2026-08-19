#!/usr/bin/env bash
# Isolated regression test for the layout store + resume-cwd fix.
# Everything runs against a throwaway tmux socket, a fake $HOME-ish project
# tree and a scratch layout DB — the live cockpit grid is never touched.
set -uo pipefail
CK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
T=$(mktemp -d "${TMPDIR:-/tmp}/cklayout.XXXXXX")
export PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex"
export COCKPIT_LAYOUT_DB="$T/layout.db"
export COCKPIT_SESSION=cktest COCKPIT_TMUX="tmux -L cktest"
export COCKPIT_REMOTE_CONTROL=0
source "$CK/lib.sh"
pass=0; fail=0
ok(){ if eval "$2"; then echo "  PASS  $1"; pass=$((pass+1)); else echo "  FAIL  $1"; fail=$((fail+1)); fi; }

# --- fixture: a session launched in $T/work that later cd'd into $T/work/sub.
mkdir -p "$T/work/sub" "$PROJECTS_DIR"
SID=11111111-2222-3333-4444-555555555555
PD="$PROJECTS_DIR/$(encode_project_dir "$T/work")"
mkdir -p "$PD"
printf '%s\n' \
  "{\"type\":\"mode\"}" \
  "{\"type\":\"system\",\"cwd\":\"$T/work\",\"sessionId\":\"$SID\"}" \
  "{\"type\":\"assistant\",\"cwd\":\"$T/work/sub\"}" > "$PD/$SID.jsonl"

# --- fixture: a valid orb MCP server spec, the shape Orbital writes per launch. cockpit_orb_read_spec
# requires an absolute, executable command, so /bin/echo stands in for the node binary.
ORB="$T/orb.server.json"
printf '{"command":"/bin/echo","args":["%s/orb-mcp.mjs"],"env":{"ORB_TOKEN_FILE":"%s/orb.token"}}\n' "$T" "$T" > "$ORB"

echo "== fix 1: resume cwd follows the transcript, not the drifted @cwd"
ok "drifted cwd is corrected to the owning dir" \
   '[[ "$(claude_launch_cwd "$SID" "$T/work/sub")" == "$T/work" ]]'
ok "a correct cwd is left alone" \
   '[[ "$(claude_launch_cwd "$SID" "$T/work")" == "$T/work" ]]'
ok "unknown session falls back to the recorded cwd" \
   '[[ "$(claude_launch_cwd deadbeef-0000-0000-0000-000000000000 "$T/nope")" == "$T/nope" ]]'
ok "resume command cds to the owning dir" \
   'agent_resume_inner claude "$SID" "$T/work/sub" x | grep -q "cd $T/work &&"'
ok "codex resume cwd is untouched" \
   'agent_resume_inner codex 019f-abc "$T/work/sub" x | grep -q "cd $T/work/sub"'
ok "codex resume disables the OS sandbox" \
   'agent_resume_inner codex 019f-abc "$T/work/sub" x | grep -q -- "--sandbox danger-full-access"'
ok "codex resume keeps automatic approval review" \
   'agent_resume_inner codex 019f-abc "$T/work/sub" x | grep -q -- "--ask-for-approval on-request -c approvals_reviewer=auto_review"'

# The orb channel is attached per invocation, in argv. A resume that rebuilds argv without it silently strips
# an execution's ask and declare tools — which is how a session came back from a reboot unable to declare while
# its spec file sat on disk. Re-attaching is conditional on the spec still validating.
echo "== fix 5: a resumed session keeps the orb channel it was launched with"
ok "claude resume re-attaches the orb server" \
   'agent_resume_inner claude "$SID" "$T/work" x "$ORB" | grep -q -- "--mcp-config"'
ok "the re-attached server is the recorded one" \
   'agent_resume_inner claude "$SID" "$T/work" x "$ORB" | grep -q "orb-mcp.mjs"'
ok "no binding leaves the resume byte-identical" \
   '[[ "$(agent_resume_inner claude "$SID" "$T/work" x)" == "$(agent_resume_inner claude "$SID" "$T/work" x "")" ]]'
ok "a vanished spec degrades to no orb, not a broken resume" \
   '[[ "$(agent_resume_inner claude "$SID" "$T/work" x "$T/gone.json")" == "$(agent_resume_inner claude "$SID" "$T/work" x)" ]]'
ok "an invalid spec is refused, not passed through" \
   'printf "{\"command\":\"not-absolute\"}" > "$T/bad.json"; [[ "$(agent_resume_inner claude "$SID" "$T/work" x "$T/bad.json")" == "$(agent_resume_inner claude "$SID" "$T/work" x)" ]]'
ok "codex resume re-attaches the orb server as -c values" \
   'agent_resume_inner codex 019f-abc "$T/work" x "$ORB" | grep -q "mcp_servers.orb.command"'

echo "== fix 3: SQLite store round-trips, keeps history, survives nasty labels"
SNAP=$'@active\tworkspace-b\n0\tworkspace-a\t'"$SID"$'\t'"$T/work"$'\tlabel with \'quotes\' and, commas\tclaude\tcpw_test\tcpp_test\t3\t7\tworking\t'"$ORB"$'\n1\tworkspace-b\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>'
ok "save accepts a snapshot"            'cockpit_layout_save "$SNAP"'
ok "load returns it verbatim"           '[[ "$(cockpit_layout_load)" == "$SNAP" ]]'
ok "single quotes survive storage"      'cockpit_layout_load | grep -q "with .quotes. and, commas"'
ok "empty fields round-trip as <nil>"   'cockpit_layout_load | grep -qP "^1\tworkspace-b\t<nil>"'
ok "controller identity survives storage" 'cockpit_layout_load | grep -q $'"'"'cpw_test\tcpp_test\t3\t7\tworking\t'"'"''
# The orb binding is what lets a RESUMED execution keep the channel it was launched with, so it has to survive
# the layout store or the resume has nothing to re-attach.
ok "orb binding survives storage"       '[[ "$(cockpit_layout_load | sed -n 2p | cut -f12)" == "$ORB" ]]'
ok "a pane with no orb binding stays empty" \
   '[[ "$(cockpit_layout_load | sed -n 3p | cut -f12)" == "<nil>" ]]'
ok "empty snapshot is refused"          '! cockpit_layout_save ""'
SNAP2=${SNAP/workspace-a/workspace-z}
cockpit_layout_save "$SNAP2" >/dev/null
ok "newest snapshot wins"               'cockpit_layout_load | grep -q workspace-z'
ok "history keeps the predecessor"      'cockpit_layout_load 1 | grep -q workspace-a'
ok "two snapshots recorded"             '[[ "$(sqlite3 "$COCKPIT_LAYOUT_DB" "SELECT COUNT(*) FROM snapshots;")" == 2 ]]'
COCKPIT_LAYOUT_KEEP=1 cockpit_layout_save "${SNAP/workspace-a/workspace-q}" >/dev/null
ok "pruning bounds the history"         '[[ "$(sqlite3 "$COCKPIT_LAYOUT_DB" "SELECT COUNT(*) FROM snapshots;")" == 1 ]]'
ok "pruning leaves no orphan panes"     '[[ "$(sqlite3 "$COCKPIT_LAYOUT_DB" "SELECT COUNT(*) FROM panes WHERE snapshot_id NOT IN (SELECT id FROM snapshots);")" == 0 ]]'

echo "== fix 2: a failing launch keeps its pane (and so its workspace)"
tmux -L cktest kill-server 2>/dev/null
BAD=$(cockpit_keep_pane_on_failure "cd $T/work && exec claude --resume $SID")
tmux -L cktest new-session -d -s cktest -n solo -x 120 -y 30 \
     "bash -lc $(printf %q "${BAD/claude --resume $SID/false}")"
sleep 2
ok "window survives a failed launch"    '[[ -n "$(tmux -L cktest list-windows -t cktest -F "#{window_name}" 2>/dev/null | grep -x solo)" ]]'
ok "failure is visible in the pane"     'tmux -L cktest capture-pane -p -t cktest 2>/dev/null | grep -q "launch failed"'
tmux -L cktest kill-server 2>/dev/null

echo "== fix 4: Codex bulk starts can be staggered without delaying other agents"
INNER='cd /tmp && exec codex resume 019f-test'
FIRST=$(COCKPIT_CODEX_STAGGER_SECS=3 cockpit_stagger_agent_command codex 0 "$INNER")
THIRD=$(COCKPIT_CODEX_STAGGER_SECS=3 cockpit_stagger_agent_command codex 2 "$INNER")
CLAUDE=$(COCKPIT_CODEX_STAGGER_SECS=3 cockpit_stagger_agent_command claude 9 'exec claude')
ok "first Codex launch is immediate"    '[[ "$FIRST" == "$INNER" ]]'
ok "later Codex launch gets ordinal delay" 'grep -q "sleep 6" <<<"$THIRD"'
ok "queued launch explains its delay"  'grep -q "startup.*queued.*6s" <<<"$THIRD"'
ok "Claude launch is never staggered"  '[[ "$CLAUDE" == "exec claude" ]]'

echo "== control: the OLD behaviour would have lost that window"
tmux -L cktest new-session -d -s cktest -n solo -x 120 -y 30 "bash -lc $(printf %q "cd $T/work && exec false")"
sleep 2
ok "unwrapped failure destroys the window (the bug)" \
   '[[ -z "$(tmux -L cktest list-windows -t cktest -F "#{window_name}" 2>/dev/null | grep -x solo)" ]]'
tmux -L cktest kill-server 2>/dev/null

echo; echo "passed=$pass failed=$fail"; rm -rf "$T"; [[ $fail == 0 ]]
