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

echo "== fix 3: SQLite store round-trips, keeps history, survives nasty labels"
SNAP=$'@active\tworkspace-b\n0\tworkspace-a\t'"$SID"$'\t'"$T/work"$'\tlabel with \'quotes\' and, commas\tclaude\n1\tworkspace-b\t<nil>\t<nil>\t<nil>\t<nil>'
ok "save accepts a snapshot"            'cockpit_layout_save "$SNAP"'
ok "load returns it verbatim"           '[[ "$(cockpit_layout_load)" == "$SNAP" ]]'
ok "single quotes survive storage"      'cockpit_layout_load | grep -q "with .quotes. and, commas"'
ok "empty fields round-trip as <nil>"   'cockpit_layout_load | grep -qP "^1\tworkspace-b\t<nil>"'
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

echo "== control: the OLD behaviour would have lost that window"
tmux -L cktest new-session -d -s cktest -n solo -x 120 -y 30 "bash -lc $(printf %q "cd $T/work && exec false")"
sleep 2
ok "unwrapped failure destroys the window (the bug)" \
   '[[ -z "$(tmux -L cktest list-windows -t cktest -F "#{window_name}" 2>/dev/null | grep -x solo)" ]]'
tmux -L cktest kill-server 2>/dev/null

echo; echo "passed=$pass failed=$fail"; rm -rf "$T"; [[ $fail == 0 ]]
