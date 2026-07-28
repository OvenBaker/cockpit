#!/usr/bin/env bash
# `cockpit-pane release` contract test. Real tmux on an isolated throwaway socket (strictly more faithful than a
# fake tmux) behind a RECORDING SHIM, so both the resulting tmux state and the exact calls made to get there are
# assertable — the shim is what lets a refusal prove it invoked nothing, and the sole-pane branch prove it
# detached cockpit-ws's reaper rather than cockpit-pane's.
#
# Evidence map:
#   BRF-001  target's OWN window resolved, both branches, both state shapes, `released` on stdout, exit 0
#            → t_shared, t_sole, t_sole_in_view
#   BRF-002  four pairwise-distinct non-zero refusals, all inert; already-marked / already-absent exit 0
#            → t_refuse_noarg, t_refuse_foreign, t_refuse_lastwin, t_refuse_notmux,
#              t_already_marked, t_already_absent, t_refusal_codes
#   BRF-003  the existing branches and the sibling scripts are behaviourally untouched; the sole-pane branch
#            detaches cockpit-ws's reaper rather than duplicating it  → t_pinned, t_sole_reaper
#   BRF-020  the positive capability probe an older checkout cannot answer  → t_capability
set -uo pipefail
REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# The commit this subcommand was authored against. Pinned as a SHA, never a moving ref: `origin/main` will
# contain this change the moment it merges, and the old-checkout probe would then be asserting against itself.
BASELINE_COMMIT="d0d71f5"

fails=0
ok()   { printf '  ok   %s\n' "$1"; }
bad()  { printf '  FAIL %s\n' "$1"; fails=$((fails + 1)); }
is()   { [[ "$2" == "$3" ]] && ok "$1" || bad "$1 (expected '$3', got '$2')"; }
isnt() { [[ "$2" != "$3" ]] && ok "$1" || bad "$1 (expected anything but '$3')"; }

root=$(mktemp -d)
socket="cprel-$RANDOM"
DEAD_SOCKET="cprel-dead-$RANDOM"
trap 'tmux -L "$socket" kill-server 2>/dev/null; tmux -L "$LAST_SOCKET" kill-server 2>/dev/null; rm -rf "$root"' EXIT
mkdir -p "$root/bin"

# Recording tmux shim: append the exact argv (NUL-separated per call, one call per line) then exec real tmux.
cat > "$root/bin/rec-tmux" <<'SHIM'
#!/usr/bin/env bash
{ printf '%s\t' "$@"; printf '\n'; } >> "$COCKPIT_TEST_TMUX_LOG"
exec tmux "$@"
SHIM
chmod +x "$root/bin/rec-tmux"

export COCKPIT_SESSION=cockpit
export COCKPIT_CLOSE_GRACE=1
LOG="$root/tmux.log"
export COCKPIT_TEST_TMUX_LOG="$LOG"
export COCKPIT_TMUX="$root/bin/rec-tmux -L $socket"
TM="tmux -L $socket"

resetlog() { : > "$LOG"; }
# The shim's argv begins with the socket flags from $COCKPIT_TMUX, so the tmux VERB is a tab-delimited field
# rather than the first one. Matching it as a field is what keeps a miscount from reading as a zero.
calls()    { grep -c "	$1	" "$LOG" 2>/dev/null || true; }
logged()   { grep -c -- "$1" "$LOG" 2>/dev/null || true; }
release()  { "$REPO/cockpit-pane" release "$@" 2>"$root/err" ; }

# Full option snapshot: every window's window-options plus the session globals. A refusal must leave this
# byte-identical.
snapshot() {
  $TM list-windows -t "$COCKPIT_SESSION" -F '#{window_id} #{window_name}' 2>/dev/null | sort
  for w in $($TM list-windows -t "$COCKPIT_SESSION" -F '#{window_id}' 2>/dev/null); do
    printf '%s ' "$w"; $TM show -w -t "$w" 2>/dev/null | sort | tr '\n' ';'; printf '\n'
  done
  $TM show -g 2>/dev/null | grep '^@' | sort
}

# ── fixture: a cockpit session whose VIEWED window is an unrelated one ──────────────────────────────────────
build_session() {
  $TM kill-server 2>/dev/null
  $TM new-session -d -s "$COCKPIT_SESSION" -n aide 'sleep 600'
  $TM new-window  -t "$COCKPIT_SESSION" -n shared 'sleep 600'
  SHARED_WIN=$($TM display -p -t "$COCKPIT_SESSION:shared" '#{window_id}')
  $TM split-window -t "$SHARED_WIN" 'sleep 600'
  # the second pane of the shared window is the release target
  SHARED_TARGET=$($TM list-panes -t "$SHARED_WIN" -F '#{pane_id}' | tail -1)
  $TM set -p -t "$SHARED_TARGET" @label 'orbital-execution-00000042'
  $TM new-window -t "$COCKPIT_SESSION" -n orbital-solo 'sleep 600'
  SOLO_WIN=$($TM display -p -t "$COCKPIT_SESSION:orbital-solo" '#{window_id}')
  SOLO_TARGET=$($TM list-panes -t "$SOLO_WIN" -F '#{pane_id}')
  AIDE_WIN=$($TM display -p -t "$COCKPIT_SESSION:aide" '#{window_id}')
  $TM select-window -t "$AIDE_WIN"            # the operator is looking at an UNRELATED window
  resetlog
}

# ── BRF-001 · shared window ─────────────────────────────────────────────────────────────────────────────────
t_shared() {
  build_session
  local aide_layout_before; aide_layout_before=$($TM display -p -t "$AIDE_WIN" '#{window_layout}')
  local out; out=$(release "$SHARED_TARGET"); local code=$?
  is  "shared: exits 0"                       "$code" "0"
  is  "shared: last stdout line is released"  "$(tail -1 <<<"$out")" "released"
  is  "shared: breaks out exactly one pane"   "$(calls break-pane)" "1"
  local hold; hold=$($TM show -gqv @undo_pane)
  local tok;  tok=$($TM show -gqv @undo_pane_tok)
  is  "shared: undo kind is pane"             "$($TM show -gqv @undo_kind)" "pane"
  is  "shared: @graveyard equals the token"   "$($TM show -wqv -t "$hold" @graveyard)" "$tok"
  is  "shared: @from_win is the TARGET's window, not the viewed one" \
      "$($TM show -wqv -t "$hold" @from_win)" "$SHARED_WIN"
  is  "shared: @held_pane recorded"           "$($TM show -wqv -t "$hold" @held_pane)" "$SHARED_TARGET"
  is  "shared: @held_label recorded"          "$($TM show -wqv -t "$hold" @held_label)" "orbital-execution-00000042"
  is  "shared: no ws-shaped state written"    "$($TM show -wqv -t "$hold" @closing)" ""
  is  "shared: viewed window's layout unchanged" \
      "$($TM display -p -t "$AIDE_WIN" '#{window_layout}')" "$aide_layout_before"
  is  "shared: re-tiles the TARGET's window"  "$(logged "select-layout	-t	$SHARED_WIN")" "1"
  is  "shared: never re-tiles the viewed window" "$(logged "select-layout	-t	$AIDE_WIN")" "0"
  is  "shared: does not switch the view"      "$(calls previous-window)" "0"
}

# ── BRF-001 · sole pane, target NOT in view ─────────────────────────────────────────────────────────────────
t_sole() {
  build_session
  local out; out=$(release "$SOLO_TARGET"); local code=$?
  is  "sole: exits 0"                          "$code" "0"
  is  "sole: last stdout line is released"     "$(tail -1 <<<"$out")" "released"
  is  "sole: breaks out exactly zero panes"    "$(calls break-pane)" "0"
  is  "sole: undo kind is ws"                  "$($TM show -gqv @undo_kind)" "ws"
  is  "sole: @undo_win is the target's window" "$($TM show -gqv @undo_win)" "$SOLO_WIN"
  is  "sole: @closing equals the token"        "$($TM show -wqv -t "$SOLO_WIN" @closing)" "$($TM show -gqv @undo_tok)"
  is  "sole: @origname recorded"               "$($TM show -wqv -t "$SOLO_WIN" @origname)" "orbital-solo"
  is  "sole: no pane-shaped state written"     "$($TM show -wqv -t "$SOLO_WIN" @graveyard)" ""
  is  "sole: previous-window calls equal 0"    "$(calls previous-window)" "0"
}

# ── BRF-003 · the sole-pane branch detaches cockpit-ws's OWN reaper ─────────────────────────────────────────
# Observable rather than asserted from source: cockpit-ws reap consults @closing and kills the WINDOW;
# cockpit-pane reap consults @graveyard on a holding window. Only the former can have run here.
t_sole_reaper() {
  build_session
  # A dedicated log: detached reapers from EARLIER cases are still counting down against the shared one, and
  # their own state reads would otherwise be attributed to this release's reaper.
  local shared_log="$LOG"
  LOG="$root/reaper.log"; export COCKPIT_TEST_TMUX_LOG="$LOG"
  release "$SOLO_TARGET" >/dev/null
  resetlog                      # from here the ONLY thing still holding this log is the detached reaper
  sleep 2.5                     # COCKPIT_CLOSE_GRACE=1 plus slack
  is  "sole reaper: the window was reaped"     "$($TM list-windows -t "$COCKPIT_SESSION" -F '#{window_id}' | grep -cx "$SOLO_WIN")" "0"
  is  "sole reaper: consulted @closing — cockpit-ws's state, not cockpit-pane's" \
      "$(logged "	@closing")" "1"
  is  "sole reaper: never consulted @graveyard" "$(logged "	@graveyard")" "0"
  is  "sole reaper: killed the window once"     "$(calls kill-window)" "1"
  LOG="$shared_log"; export COCKPIT_TEST_TMUX_LOG="$LOG"
}

# ── BRF-001 · sole pane, target IN view ─────────────────────────────────────────────────────────────────────
t_sole_in_view() {
  build_session
  $TM select-window -t "$SOLO_WIN"
  resetlog
  release "$SOLO_TARGET" >/dev/null
  is  "sole in view: previous-window calls equal exactly 1" "$(calls previous-window)" "1"
}

# ── BRF-002 · refusals are inert and non-zero ───────────────────────────────────────────────────────────────
# Each refusal asserts: distinct non-zero code, non-empty stderr, zero break-pane, zero kill-window, a
# byte-identical option snapshot, and no invocation naming the current pane or window.
assert_inert() {
  local name="$1" before="$2"
  is   "$name: break-pane calls equal 0"   "$(calls break-pane)" "0"
  is   "$name: kill-window calls equal 0"  "$(calls kill-window)" "0"
  is   "$name: stderr is non-empty"        "$([[ -s "$root/err" ]] && echo yes || echo no)" "yes"
  is   "$name: option snapshot unchanged"  "$(snapshot | md5sum)" "$before"
  is   "$name: names neither the current window nor the current pane" \
       "$(( $(logged "$AIDE_WIN") + $(logged "$($TM display -p -t "$AIDE_WIN" '#{pane_id}')") ))" "0"
}

t_refuse_noarg() {
  build_session
  local before; before=$(snapshot | md5sum)
  release >/dev/null; CODE_NOARG=$?
  isnt "no-arg: code is non-zero"          "$CODE_NOARG" "0"
  isnt "no-arg: code is not the usage fall-through" "$CODE_NOARG" "2"
  assert_inert "no-arg" "$before"
}

t_refuse_foreign() {
  build_session
  $TM new-session -d -s stranger 'sleep 600'
  local foreign; foreign=$($TM list-panes -t stranger -F '#{pane_id}')
  resetlog
  local before; before=$(snapshot | md5sum)
  release "$foreign" >/dev/null; CODE_FOREIGN=$?
  isnt "foreign: code is non-zero"         "$CODE_FOREIGN" "0"
  isnt "foreign: code is not the usage fall-through" "$CODE_FOREIGN" "2"
  assert_inert "foreign" "$before"
  $TM kill-session -t stranger 2>/dev/null
}

t_refuse_lastwin() {
  LAST_SOCKET="cprel-last-$RANDOM"
  local saved="$COCKPIT_TMUX"
  export COCKPIT_TMUX="$root/bin/rec-tmux -L $LAST_SOCKET"
  local LT="tmux -L $LAST_SOCKET"
  $LT new-session -d -s "$COCKPIT_SESSION" -n only 'sleep 600'
  local pane; pane=$($LT list-panes -t "$COCKPIT_SESSION" -F '#{pane_id}')
  resetlog
  local before; before=$($LT show -w -t "$COCKPIT_SESSION:only" 2>/dev/null | sort | md5sum)
  release "$pane" >/dev/null; CODE_LASTWIN=$?
  isnt "last-window: code is non-zero"     "$CODE_LASTWIN" "0"
  isnt "last-window: code is not the usage fall-through" "$CODE_LASTWIN" "2"
  is   "last-window: break-pane calls equal 0"  "$(calls break-pane)" "0"
  is   "last-window: kill-window calls equal 0" "$(calls kill-window)" "0"
  is   "last-window: stderr is non-empty"  "$([[ -s "$root/err" ]] && echo yes || echo no)" "yes"
  is   "last-window: window options unchanged" \
       "$($LT show -w -t "$COCKPIT_SESSION:only" 2>/dev/null | sort | md5sum)" "$before"
  $LT kill-server 2>/dev/null
  export COCKPIT_TMUX="$saved"
}

t_refuse_notmux() {
  local saved="$COCKPIT_TMUX"
  export COCKPIT_TMUX="$root/bin/rec-tmux -L $DEAD_SOCKET"
  resetlog
  release '%99' >/dev/null; CODE_NOTMUX=$?
  isnt "no-tmux: code is non-zero"         "$CODE_NOTMUX" "0"
  isnt "no-tmux: code is not the usage fall-through" "$CODE_NOTMUX" "2"
  is   "no-tmux: break-pane calls equal 0" "$(calls break-pane)" "0"
  is   "no-tmux: does not converge to already-absent on a dead server" \
       "$(release '%99'; echo "exit=$?")" "exit=$CODE_NOTMUX"
  is   "no-tmux: stderr is non-empty"      "$([[ -s "$root/err" ]] && echo yes || echo no)" "yes"
  export COCKPIT_TMUX="$saved"
}

t_refusal_codes() {
  local codes="$CODE_NOARG $CODE_FOREIGN $CODE_LASTWIN $CODE_NOTMUX"
  is "refusal codes are pairwise distinct" "$(tr ' ' '\n' <<<"$codes" | sort -u | wc -l)" "4"
}

# ── BRF-002 · the two committed-already outcomes exit 0 ─────────────────────────────────────────────────────
t_already_marked() {
  build_session
  # A pane already broken out to a holding window, carrying SOMEBODY ELSE'S token.
  local held; held=$($TM break-pane -d -s "$SHARED_TARGET" -P -F '#{pane_id}')
  local hold; hold=$($TM display -p -t "$held" '#{window_id}')
  $TM set -w -t "$hold" @graveyard "a-foreign-token"
  resetlog
  local before; before=$(snapshot | md5sum)
  local out; out=$(release "$held"); local code=$?
  is "already-marked: exits 0"                     "$code" "0"
  is "already-marked: last stdout line"            "$(tail -1 <<<"$out")" "already-marked"
  is "already-marked: writes no state"             "$(snapshot | md5sum)" "$before"
  is "already-marked: the foreign token is intact" "$($TM show -wqv -t "$hold" @graveyard)" "a-foreign-token"
  is "already-marked: break-pane calls equal 0"    "$(calls break-pane)" "0"

  # A window already counting down under cockpit-ws's shape is the same story from the other side.
  $TM set -w -t "$SOLO_WIN" @closing "another-foreign-token"
  resetlog
  local out2; out2=$(release "$SOLO_TARGET"); local code2=$?
  is "already-closing: exits 0"                    "$code2" "0"
  is "already-closing: last stdout line"           "$(tail -1 <<<"$out2")" "already-marked"
  is "already-closing: token intact"               "$($TM show -wqv -t "$SOLO_WIN" @closing)" "another-foreign-token"
}

t_already_absent() {
  build_session
  local before; before=$(snapshot | md5sum)
  local out; out=$(release '%987654'); local code=$?
  is "already-absent: exits 0"          "$code" "0"
  is "already-absent: last stdout line" "$(tail -1 <<<"$out")" "already-absent"
  is "already-absent: writes no state"  "$(snapshot | md5sum)" "$before"
}

# ── BRF-020 · the positive capability probe ─────────────────────────────────────────────────────────────────
t_capability() {
  local out; out=$("$REPO/cockpit-pane" release --capability 2>"$root/err"); local code=$?
  is   "capability: exits 0"                   "$code" "0"
  is   "capability: prints its own token"      "$(tail -1 <<<"$out")" "cockpit-pane-release/1"
  # The older checkout: the real file at the commit this was authored against, which answers an unknown
  # subcommand with the usage fall-through — observably a refusal, which is exactly why a POSITIVE token is
  # the only distinguishing evidence.
  local old="$root/old-cockpit-pane"
  if ! git -C "$REPO" show "$BASELINE_COMMIT:cockpit-pane" > "$old" 2>/dev/null; then
    bad "capability: baseline commit $BASELINE_COMMIT is not available in this checkout"
    return
  fi
  chmod +x "$old"
  local oldout; oldout=$("$old" release --capability 2>/dev/null); local oldcode=$?
  isnt "capability: an older checkout exits non-zero" "$oldcode" "0"
  is   "capability: an older checkout prints no token" \
       "$(grep -c 'cockpit-pane-release' <<<"$oldout")" "0"
}

# ── BRF-003 · nothing else moved ────────────────────────────────────────────────────────────────────────────
# The existing case bodies and the sibling scripts are pinned by content hash against the commit this
# subcommand was authored on top of. A pinned SHA, not a moving ref: `origin/main` acquires this change on
# merge and would then compare the file against itself.
branch_body() { awk -v want="  $2)" '$0 == want {on=1; next} on && /^  [a-z*]+\)/ {exit} on {print}' "$1"; }

t_pinned() {
  local base="$root/base"; mkdir -p "$base"
  for f in cockpit-pane cockpit-ws cockpit-undo cockpit-spawn; do
    if ! git -C "$REPO" show "$BASELINE_COMMIT:$f" > "$base/$f" 2>/dev/null; then
      bad "pinned: baseline commit $BASELINE_COMMIT is not available in this checkout"; return
    fi
  done
  for branch in remove undo reap add; do
    is "pinned: cockpit-pane's $branch branch is byte-identical" \
       "$(branch_body "$REPO/cockpit-pane" "$branch" | md5sum)" \
       "$(branch_body "$base/cockpit-pane" "$branch" | md5sum)"
  done
  for f in cockpit-ws cockpit-undo cockpit-spawn; do
    is "pinned: $f is byte-identical" "$(md5sum < "$REPO/$f")" "$(md5sum < "$base/$f")"
  done
  is "pinned: exactly one executable changed" \
     "$(git -C "$REPO" diff --name-only "$BASELINE_COMMIT" -- . ':!tests' | wc -l)" "1"
}

printf 'cockpit-pane release\n'
t_shared
t_sole
t_sole_reaper
t_sole_in_view
t_refuse_noarg
t_refuse_foreign
t_refuse_lastwin
t_refuse_notmux
t_refusal_codes
t_already_marked
t_already_absent
t_capability
t_pinned

if (( fails > 0 )); then printf '\n%d failure(s)\n' "$fails"; exit 1; fi
printf '\nall pane-release checks passed\n'
