#!/usr/bin/env bash
# Regression test for the 2026-08-12 grid loss: 23 of 24 workspaces closed inside
# one grace period and only one was recoverable, then restore rebuilt the wreck.
#
# Three defects, three sections:
#   A  the "can't close the last workspace" guard counted windows already counting
#      down, so a burst could mark every workspace in the grid
#   B  the undo slot was a single global, so a burst left N-1 closes orphaned while
#      they were still alive and undoable
#   C  restore takes the newest snapshot with no regard for a catastrophic shrink,
#      and offsets are a moving target so there was no stable way to name a good one
#
# Throwaway tmux socket, sandboxed HOME, scratch layout DB — the live grid is never
# touched.
set -uo pipefail
CK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
T=$(mktemp -d "${TMPDIR:-/tmp}/ckburst.XXXXXX")
SOCK="ckburst-$RANDOM"
export PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex"
export COCKPIT_LAYOUT_DB="$T/layout.db"
export COCKPIT_SESSION="$SOCK" COCKPIT_TMUX="tmux -L $SOCK"
export COCKPIT_REMOTE_CONTROL=0
TM="tmux -L $SOCK"
trap '$TM kill-server 2>/dev/null; pkill -f "$T" 2>/dev/null; rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/codex" "$T/projects" "$T/work"

pass=0; fail=0
ok(){ if eval "$2"; then echo "  PASS  $1"; pass=$((pass+1)); else echo "  FAIL  $1"; fail=$((fail+1)); fi; }
is(){ [[ "$2" == "$3" ]] && { echo "  PASS  $1"; pass=$((pass+1)); } || { echo "  FAIL  $1 (expected '$3', got '$2')"; fail=$((fail+1)); }; }

source "$CK/lib.sh"

# ── A + B: close burst and undo depth ────────────────────────────────────────
# A long grace keeps every close pending for the whole section, which is exactly
# the window the real incident happened in.
export COCKPIT_CLOSE_GRACE=120
$TM new-session -d -s "$COCKPIT_SESSION" -n alpha 'sleep 600'
$TM new-window  -t "$COCKPIT_SESSION" -n bravo   'sleep 600'
$TM new-window  -t "$COCKPIT_SESSION" -n charlie 'sleep 600'

echo "== A: a close burst cannot mark the last workspace"
for _ in 1 2 3 4 5; do "$CK/cockpit-ws" close >/dev/null 2>&1; done
marked=$($TM list-windows -t "$COCKPIT_SESSION" -F '#{@closing}' | grep -c .)
alive=$($TM list-windows -t "$COCKPIT_SESSION" -F '#{@closing}' | grep -c '^$')
is "5 closes over 3 workspaces mark only 2" "$marked" "2"
is "one workspace is left unmarked"         "$alive"  "1"
is "no window was actually killed yet"      "$($TM list-windows -t "$COCKPIT_SESSION" -F x | wc -l)" "3"
ok "the survivor is the one still named plainly" \
   '$TM list-windows -t "$COCKPIT_SESSION" -F "#{@closing}#{window_name}" | grep -qx alpha'

echo "== B: every pending close is recoverable, newest first"
is "two closes are pending"       "$(cockpit_ws_undo_pending)" "2"
"$CK/cockpit-ws" undo >/dev/null 2>&1
is "one close left after one undo" "$(cockpit_ws_undo_pending)" "1"
"$CK/cockpit-ws" undo >/dev/null 2>&1
is "none left after the second undo" "$(cockpit_ws_undo_pending)" "0"
is "all three workspaces are unmarked again" \
   "$($TM list-windows -t "$COCKPIT_SESSION" -F '#{@closing}' | grep -c '^$')" "3"
ok "names were restored, not left as ✗ markers" \
   '! $TM list-windows -t "$COCKPIT_SESSION" -F "#{window_name}" | grep -q "✗"'
"$CK/cockpit-ws" undo >/dev/null 2>&1
is "a third undo finds nothing and clears the stack" "$($TM show -gqv @undo_ws_stack)" ""

echo "== B2: the legacy single-slot mirror still tracks the stack top"
"$CK/cockpit-ws" close >/dev/null 2>&1
top_win=$($TM show -gqv @undo_win); top_tok=$($TM show -gqv @undo_tok)
ok "@undo_win names a window that is really closing" \
   '[[ -n "$top_win" && "$($TM show -wqv -t "$top_win" @closing)" == "$top_tok" ]]'
is "@undo_kind still routes Alt-u to the ws handler" "$($TM show -gqv @undo_kind)" "ws"
"$CK/cockpit-ws" undo >/dev/null 2>&1
is "the mirror empties with the stack" "$($TM show -gqv @undo_win)" ""
$TM kill-server 2>/dev/null

# ── C: restore notices a collapsed layout and can be aimed by id ──────────────
# Stub agent: `--resume <id>` only resolves ids whose transcript sits under the
# cwd's project dir, same rule the real one enforces.
cat > "$T/bin/claude" <<STUB
#!/usr/bin/env bash
id=""; prev=""; for a in "\$@"; do [[ "\$prev" == --resume ]] && id="\$a"; prev="\$a"; done
enc=\$(echo "\$PWD" | sed 's#[/.]#-#g')
[[ -f "$T/projects/\$enc/\$id.jsonl" ]] || { echo "no such session"; exit 1; }
exec sleep 600
STUB
chmod +x "$T/bin/claude"
printf 'export PATH=%s/bin:$PATH\n' "$T" > "$T/.bash_profile"; cp "$T/.bash_profile" "$T/.profile"

mk(){ local pd="$T/projects/$(encode_project_dir "$T/work")"; mkdir -p "$pd"
      printf '{"type":"system","cwd":"%s","sessionId":"%s"}\n' "$T/work" "$1" > "$pd/$1.jsonl"; }
S1=aaaaaaaa-0000-0000-0000-000000000001
S2=bbbbbbbb-0000-0000-0000-000000000002
S3=cccccccc-0000-0000-0000-000000000003
mk "$S1"; mk "$S2"; mk "$S3"

row(){ printf '%s\t%s\t%s\t%s\t%s\tclaude\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>' "$1" "$2" "$3" "$T/work" "$2"; }
RICH=$'@active\tone\n'"$(row 0 one "$S1")"$'\n'"$(row 1 two "$S2")"$'\n'"$(row 2 three "$S3")"
POOR=$'@active\tthree\n'"$(row 0 three "$S3")"
cockpit_layout_save "$RICH" || { echo "seed failed"; exit 1; }
RICH_ID=$(sqlite3 "$COCKPIT_LAYOUT_DB" "SELECT MAX(id) FROM snapshots;")
sleep 1                                    # snapshots are ordered by `at` (1s resolution)
cockpit_layout_save "$POOR" || { echo "seed failed"; exit 1; }

echo "== C: the shrink is reported instead of silently rebuilt"
is "stats read the newest (collapsed) snapshot" "$(cockpit_layout_stats 0 | cut -f2)" "1"
is "peak finds the rich snapshot"               "$(cockpit_layout_peak 30 | cut -f1)" "$RICH_ID"

run_restore(){ env HOME="$T" PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex" \
  COCKPIT_LAYOUT_DB="$T/layout.db" COCKPIT_SESSION="$COCKPIT_SESSION" COCKPIT_TMUX="$COCKPIT_TMUX" \
  COCKPIT_REMOTE_CONTROL=0 COCKPIT_NO_SINGLETON=1 "$CK/cockpit" "$@" </dev/null >"$T/out" 2>&1; }

run_restore --restore; sleep 2
ok "the warning names the fuller snapshot and how to reach it" \
   'grep -q -- "--restore-id $RICH_ID" "$T/out"'
ok "the warning states both sizes" 'grep -q "newest layout has 1 workspace" "$T/out"'
is "it still restored the newest, unprompted" \
   "$($TM list-windows -t "$COCKPIT_SESSION" -F x | wc -l)" "1"
$TM kill-server 2>/dev/null; sleep 1

echo "== C2: --restore-id rebuilds the named snapshot"
run_restore --restore-id "$RICH_ID"; sleep 3
is "all three workspaces came back" "$($TM list-windows -t "$COCKPIT_SESSION" -F x | wc -l)" "3"
ok "workspace names match the snapshot" \
   '[[ "$($TM list-windows -t "$COCKPIT_SESSION" -F "#{window_name}" | sort | tr "\n" " ")" == "one three two " ]]'
ok "no shrink warning when an id was named explicitly" \
   '! grep -q "restore-id" "$T/out"'
$TM kill-server 2>/dev/null

echo "== C3: a bad id is refused, not silently downgraded"
run_restore --restore-id 999999 || true
ok "unknown snapshot id is reported" 'grep -q "no snapshot 999999" "$T/out"'

echo
echo "ws-close-burst: $pass passed, $fail failed"
exit $(( fail > 0 ))
