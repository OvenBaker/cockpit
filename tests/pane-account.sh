#!/usr/bin/env bash
# Per-pane Claude account contract. Real tmux on a throwaway socket, a sandboxed HOME, a scratch layout DB
# and a scratch COCKPIT_ACCOUNTS_DIR. Never touches the live cockpit server or the operator's real accounts.
#
# What this pins down, and why each one matters:
#   1  a token file that is missing / empty / whitespace-only / group-or-other readable / a symlink /
#      not a regular file is REFUSED, non-zero, with no pane created.  The empty case is THE hazard: Claude
#      Code reads CLAUDE_CODE_OAUTH_TOKEN="" as UNSET and silently runs on the PRIMARY account, so a
#      pass-through would burn the exact quota the binding exists to protect with nothing on screen.
#   2  an out-of-shape account NAME is refused (it becomes a filename and a tmux option value).
#   3  --account with --agent codex or --agent shell is refused, never silently ignored — a caller who asked
#      for the second subscription and got the first has no way to see that from the outside.
#   4  a VALID token: the pane comes up, @account carries the NAME, the token reaches the CHILD PROCESS's
#      environment, and appears NOWHERE else — not in the pane's start command, any tmux option, the tmux
#      environment, cockpit-state, the layout snapshot, the layout DB, or any file cockpit wrote.
#   5  snapshot → layout DB → restore round-trips @account, and the restored pane's child gets the token.
#   6  a restored pane whose @account no longer resolves comes up REFUSING, visibly, with the provider never
#      started — instead of quietly resuming on the default account.
set -uo pipefail
CK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOCK="ckacct-$$-$RANDOM"
SESS=ckacct
T=$(mktemp -d "${TMPDIR:-/tmp}/ckacct.XXXXXX")
TMUX="tmux -L $SOCK"
# A sentinel that cannot occur by accident, so a single grep is a real leak test.
TOKEN='sk-ant-oat01-COCKPITxTESTxSENTINELx0123456789'
cleanup() { $TMUX kill-server 2>/dev/null; pkill -f "$T" 2>/dev/null; rm -rf "$T"; }
trap cleanup EXIT

mkdir -p "$T/bin" "$T/accounts" "$T/projects" "$T/codex" "$T/work" "$T/out" "$T/tmp"

fail=0
chk() { if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; fail=1; fi; }

# ── the environment every cockpit invocation runs under ────────────────────────────────────────────────────
# HOME is sandboxed so the stub `claude` below wins PATH inside the pane's `bash -l`, and so nothing here can
# read or write the operator's real ~/.config/cockpit/accounts.
ENVV=(env HOME="$T" TMPDIR="$T/tmp" PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex"
      SANTA_DB="$T/santa.db" COCKPIT_LAYOUT_DB="$T/layout.db"
      COCKPIT_SESSION="$SESS" COCKPIT_TMUX="$TMUX" COCKPIT_ACCOUNTS_DIR="$T/accounts"
      COCKPIT_REMOTE_CONTROL=0 COCKPIT_NO_SINGLETON=1)

# Stub provider: records that it ran, its exact argv, and the CLAUDE_CODE_OAUTH_TOKEN it was handed. The
# token record is the ONLY place in $T the token is allowed to appear besides its own token file — it is the
# positive evidence that `-e` actually delivered it to the child.
cat > "$T/bin/claude" <<STUB
#!/usr/bin/env bash
echo start >> "$T/out/starts"
: > "$T/out/argv"; for a in "\$@"; do printf '%s\0' "\$a" >> "$T/out/argv"; done
printf '%s\n' "\${CLAUDE_CODE_OAUTH_TOKEN:-<unset>}" >> "$T/out/tokens"
exec sleep 600
STUB
chmod +x "$T/bin/claude"
printf 'export PATH=%s/bin:$PATH\n' "$T" > "$T/.bash_profile"
cp "$T/.bash_profile" "$T/.profile"

source "$CK/lib.sh"
mk() {  # fabricate a Claude transcript so restore/resume treat the id as real
  local pd="$T/projects/$(encode_project_dir "$T/work")"
  mkdir -p "$pd"; printf '{"type":"system","cwd":"%s","sessionId":"%s"}\n' "$T/work" "$1" > "$pd/$1.jsonl"
}

# ── token-file fixtures, one per refusal shape ─────────────────────────────────────────────────────────────
printf '%s\n' "$TOKEN"        > "$T/accounts/good.token";   chmod 600 "$T/accounts/good.token"
: >                             "$T/accounts/empty.token";  chmod 600 "$T/accounts/empty.token"
printf '   \n\t\n'            > "$T/accounts/blank.token";  chmod 600 "$T/accounts/blank.token"
printf '%s\n' "$TOKEN"        > "$T/accounts/loose.token";  chmod 644 "$T/accounts/loose.token"
printf '%s\n' "$TOKEN"        > "$T/accounts/grouped.token";chmod 640 "$T/accounts/grouped.token"
ln -s "$T/accounts/good.token"  "$T/accounts/linked.token"
mkdir -p                        "$T/accounts/adir.token"

$TMUX kill-server 2>/dev/null
"${ENVV[@]}" $TMUX new-session -d -s "$SESS" -n work 'sleep 600'

panes() { $TMUX list-panes -s -t "$SESS" -F '#{pane_id}' 2>/dev/null | wc -l; }
starts() { wc -l < "$T/out/starts" 2>/dev/null || echo 0; }
spawn() { "${ENVV[@]}" "$CK/cockpit-spawn" --cwd "$T/work" "$@" 2>"$T/out/err"; }
send()  { "${ENVV[@]}" "$CK/cockpit-send" "$@" 2>"$T/out/err"; }

# ── 1/2/3: every refusal shape, asserted on rc AND on "no pane appeared" ───────────────────────────────────
refuses() {  # description  expected-stderr-substring  args…
  local what="$1" want="$2"; shift 2
  local before after rc
  before=$(panes); spawn "$@" >/dev/null; rc=$?; after=$(panes)
  chk "refused: $what" "[[ $rc -ne 0 && $before -eq $after ]] && grep -qF $(printf %q "$want") '$T/out/err'"
}

refuses "missing token file"          "no token file"              --account nosuch
refuses "EMPTY token file (the silent-fallback hazard)" \
                                      "is read as UNSET"           --account empty
refuses "whitespace-only token file"  "is read as UNSET"           --account blank
refuses "world-readable token file"   "group/other accessible"     --account loose
refuses "group-readable token file"   "group/other accessible"     --account grouped
refuses "symlinked token file"        "is a symlink"               --account linked
refuses "token path is a directory"   "not a regular file"         --account adir
refuses "account name with a slash"   "is not [A-Za-z0-9]"         --account "../good"
refuses "account name with a space"   "is not [A-Za-z0-9]"         --account "two words"
refuses "account name starting with -" "is not [A-Za-z0-9]"        --account "-lead"
refuses "account name over 32 chars"  "is not [A-Za-z0-9]"         --account "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
refuses "--account with --agent codex" "Claude-only"               --account good --agent codex
refuses "--account with --agent shell" "Claude-only"               --account good --agent shell

# cockpit-send refuses on the same rules, and refuses a codex-resolved session by name rather than ignoring it.
before=$(panes)
send 11111111-2222-3333-4444-555555555555 --agent codex --account good >/dev/null; rc=$?
chk "cockpit-send: --account refused for a codex session" \
    "[[ $rc -ne 0 && $before -eq $(panes) ]] && grep -q 'Claude-only' '$T/out/err'"
send 11111111-2222-3333-4444-555555555555 --account empty >/dev/null; rc=$?
chk "cockpit-send: empty token file refused" \
    "[[ $rc -ne 0 && $before -eq $(panes) ]] && grep -q 'is read as UNSET' '$T/out/err'"
send 11111111-2222-3333-4444-555555555555 --account 'bad name' >/dev/null; rc=$?
chk "cockpit-send: malformed account name refused" \
    "[[ $rc -ne 0 && $before -eq $(panes) ]] && grep -q 'is not \[A-Za-z0-9\]' '$T/out/err'"

# ── 4: a valid account — the pane comes up, the CHILD gets the token, nothing else does ────────────────────
: > "$T/out/starts"; : > "$T/out/tokens"
pane=$(spawn --account good --workspace acctws --name acct-demo)
chk "valid account: pane created" '[[ "$pane" == %* ]]'
chk "valid account: @account carries the NAME" \
    "[[ \"\$($TMUX show -p -t '$pane' @account 2>/dev/null | awk '{print \$2}')\" == good ]]"

for _ in $(seq 1 50); do [[ -s "$T/out/tokens" ]] && break; sleep 0.2; done
chk "valid account: the token reached the pane's CHILD PROCESS environment" \
    "[[ \$(grep -cxF '$TOKEN' '$T/out/tokens') -eq 1 ]]"

# The binding is PER PANE. tmux's `-e` on split-window/new-window/respawn-pane is process-scoped, but on
# new-session it lands in the SESSION environment, where it is both readable and inherited by every pane
# created afterwards — which would put unbound panes on the second account, silently. Assert both halves.
chk "the token is NOT in the tmux session/global environment" \
    "! $TMUX show-environment -t '$SESS' 2>/dev/null | grep -q CLAUDE_CODE_OAUTH_TOKEN \
     && ! $TMUX show-environment -g 2>/dev/null | grep -q CLAUDE_CODE_OAUTH_TOKEN"
unbound_pane=$(spawn --workspace acctws --name plain-demo)
for _ in $(seq 1 50); do [[ $(wc -l < "$T/out/tokens") -ge 2 ]] && break; sleep 0.2; done
chk "a pane spawned AFTER a bound one does not inherit the token" \
    "[[ \$(grep -cxF '<unset>' '$T/out/tokens') -eq 1 && \$(grep -cxF '$TOKEN' '$T/out/tokens') -eq 1 ]]"
# `show -pv`, not `show -p | awk '{print $2}'`: an option explicitly cleared to "" prints as `@account ''`.
acct_of() { $TMUX show -pv -t "$1" @account 2>/dev/null; }
chk "an unbound pane carries no @account stamp" '[[ -z "$(acct_of "$unbound_pane")" ]]'
chk "an empty @account reads as UNSET to the snapshot format" \
    "[[ \"\$($TMUX display -p -t '$unbound_pane' '#{?@account,SET,UNSET}')\" == UNSET ]]"

# The leak sweep: everything tmux will tell us about this grid, plus every cockpit-produced projection of it.
haystack() {
  $TMUX list-panes -s -t "$SESS" -F '#{pane_id}|#{pane_start_command}|#{pane_current_command}|#{pane_title}'
  local p
  for p in $($TMUX list-panes -s -t "$SESS" -F '#{pane_id}'); do $TMUX show -p -t "$p" 2>/dev/null; done
  $TMUX show -g 2>/dev/null; $TMUX show -gw 2>/dev/null
  $TMUX show-environment -t "$SESS" 2>/dev/null; $TMUX show-environment -g 2>/dev/null
  "${ENVV[@]}" "$CK/cockpit-state" 2>/dev/null
  "${ENVV[@]}" bash -c "source '$CK/lib.sh'; cockpit_snapshot" 2>/dev/null
}
haystack > "$T/out/haystack"
chk "no leak: token absent from pane commands, tmux options, tmux env, cockpit-state and the snapshot" \
    "! grep -qF '$TOKEN' '$T/out/haystack'"

chk "cockpit-state reports the account NAME per pane" \
    "[[ \"\$(\"\${ENVV[@]}\" \"$CK/cockpit-state\" | jq -r --arg p '$pane' '.panes[]|select(.pane_id==\$p)|.account')\" == good ]]"
chk "cockpit-state reports \"\" for an unbound pane" \
    "[[ -z \"\$(\"\${ENVV[@]}\" \"$CK/cockpit-state\" | jq -r '.panes[]|select(.pane_id!=\"$pane\")|.account' | tr -d '\n')\" ]]"

# Persist the live grid, then prove the DB file itself has no token in it.
"${ENVV[@]}" bash -c "source '$CK/lib.sh'; cockpit_layout_save \"\$(cockpit_snapshot)\"" >/dev/null 2>&1
chk "no leak: the layout DB stores the account NAME, not the token" \
    "grep -aqF 'good' '$T/layout.db' && ! grep -aqF '$TOKEN' '$T/layout.db'"

# Nothing cockpit wrote anywhere under the sandbox may carry the token, except the token file itself and the
# stub's deliberate delivery receipt.
leaked=$(grep -rlF "$TOKEN" "$T" 2>/dev/null | grep -v "^$T/accounts/" | grep -v "^$T/out/tokens$" | grep -v "^$T/out/haystack")
chk "no leak: no other file under the sandbox contains the token" '[[ -z "$leaked" ]]'
[[ -n "$leaked" ]] && printf '      leaked into: %s\n' $leaked

# A pane REUSED by a spawn with no --account must LOSE the previous binding's stamp. Leaving it would claim a
# subscription whose token was never applied to the process now running — the surfaces would say "second
# account" while the work billed the first, and the next restore would act on the claim. A fresh workspace's
# placeholder pane has never had @session_id set, which is the shape cockpit-spawn reuses.
$TMUX new-window -t "$SESS" -d -n reusews 'sleep 600'
rp=$($TMUX list-panes -t "$SESS:reusews" -F '#{pane_id}' 2>/dev/null | head -1)
$TMUX set -p -t "$rp" @account good
reused=$(spawn --workspace reusews --name reuse-demo)
chk "the placeholder pane really was reused (not split beside)" '[[ "$reused" == "$rp" ]]'
chk "a reused pane's stale @account is CLEARED by a spawn with no --account" '[[ -z "$(acct_of "$rp")" ]]'

# ── 5/6: snapshot → restore round-trip, good account and broken account side by side ───────────────────────
A=aaaaaaaa-0000-0000-0000-00000000000a   # bound to `good`  → must resume, with the token
B=bbbbbbbb-0000-0000-0000-00000000000b   # bound to `gone`  → must REFUSE, provider never started
C=cccccccc-0000-0000-0000-00000000000c   # unbound          → ordinary pane, unchanged behaviour
mk "$A"; mk "$B"; mk "$C"
rm -f "$T"/layout.db*            # the -wal/-shm siblings too, or the fresh DB opens against a stale WAL
# `<nil>` placeholders, NOT empty fields: tab is an IFS whitespace character, so `read` collapses a run of
# them into one delimiter and every later column shifts left. That is exactly why the real snapshot format
# carries explicit placeholders — a fixture with bare tabs would silently test a different row shape.
row() { printf '0\tmain\t%s\t%s\t%s\tclaude\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>\t<nil>\t%s\n' "$1" "$T/work" "$2" "$3"; }
SNAP=$'@active\tmain\n'"$(row "$A" bound-good good)"$'\n'"$(row "$B" bound-gone gone)"$'\n'"$(row "$C" unbound '<nil>')"
"${ENVV[@]}" bash -c "source '$CK/lib.sh'; cockpit_layout_save $(printf %q "$SNAP")" \
  || { echo "FAIL  layout save"; fail=1; }

back=$("${ENVV[@]}" bash -c "source '$CK/lib.sh'; cockpit_layout_load 0")
chk "round-trip: the saved snapshot reads back with @account in field 13" \
    "[[ \"\$(awk -F'\t' '\$5==\"bound-good\"{print \$13}' <<<\"\$back\")\" == good ]]"
chk "round-trip: an unbound pane reads back as <nil>, not as some other pane's account" \
    "[[ \"\$(awk -F'\t' '\$5==\"unbound\"{print \$13}' <<<\"\$back\")\" == '<nil>' ]]"

$TMUX kill-server 2>/dev/null
: > "$T/out/starts"; : > "$T/out/tokens"
# bound-good is the FIRST row, so it is the pane `new-session` creates — the exact case where a naive `-e`
# would have put the token in the session environment for every later pane to inherit.
"${ENVV[@]}" "$CK/cockpit" --restore </dev/null >"$T/out/restore" 2>&1
for _ in $(seq 1 50); do [[ $(wc -l < "$T/out/tokens" 2>/dev/null || echo 0) -ge 2 ]] && break; sleep 0.2; done
sleep 1

pane_of() { $TMUX list-panes -s -t "$SESS" -f "#{==:#{@label},$1}" -F '#{pane_id}' 2>/dev/null | head -1; }
pg=$(pane_of bound-good); pb=$(pane_of bound-gone); pu=$(pane_of unbound)

chk "restore: all three panes rebuilt" '[[ "$(panes)" == 3 ]]'
chk "restore: the bound pane keeps @account" "[[ \"\$(acct_of '$pg')\" == good ]]"
chk "restore: the UNRESOLVABLE pane keeps @account too (so the operator can fix and retry)" \
    "[[ \"\$(acct_of '$pb')\" == gone ]]"
chk "restore: the unbound pane has no @account" "[[ -z \"\$(acct_of '$pu')\" ]]"
chk "restore: the bound pane's child got the token again" \
    "[[ \$(grep -cxF '$TOKEN' '$T/out/tokens') -eq 1 ]]"
chk "restore: the UNBOUND pane's child did NOT inherit it (new-session -e would have leaked it)" \
    "[[ \$(grep -cxF '<unset>' '$T/out/tokens') -eq 1 ]]"
chk "restore: the token is not in the rebuilt session's tmux environment" \
    "! $TMUX show-environment -t '$SESS' 2>/dev/null | grep -q CLAUDE_CODE_OAUTH_TOKEN"
# Two panes could legitimately start the provider (bound-good and unbound). The refusing one must not, or the
# whole point is lost: that start would be on the PRIMARY account.
chk "restore: exactly the two resolvable panes started the provider — the refusing one did not" \
    "[[ \$(starts) -eq 2 ]]"
sleep 0.5
$TMUX capture-pane -p -t "$pb" > "$T/out/refusal" 2>/dev/null
chk "restore: the refusing pane says WHY, on screen, naming the account" \
    "grep -q 'bound to Claude account' '$T/out/refusal' && grep -q 'gone' '$T/out/refusal'"
chk "restore: the refusing pane refuses the DEFAULT account explicitly" \
    "grep -q 'DEFAULT account' '$T/out/refusal'"
chk "restore: the refusing pane is still alive (the workspace survives)" \
    "[[ -n \"\$($TMUX list-panes -s -t '$SESS' -F '#{pane_id}' | grep -Fx '$pb')\" ]]"

haystack > "$T/out/haystack2"
chk "no leak after restore: the token is absent from every tmux and cockpit surface" \
    "! grep -qF '$TOKEN' '$T/out/haystack2'"

# ── 7: cockpit-reboot re-execs the provider, so it is the same hazard by another door ──────────────────────
$TMUX set -p -t "$pg" @state idle; $TMUX set -p -t "$pb" @state idle
: > "$T/out/tokens"
"${ENVV[@]}" "$CK/cockpit-reboot" "$pg" >"$T/out/rb" 2>&1
for _ in $(seq 1 50); do [[ -s "$T/out/tokens" ]] && break; sleep 0.2; done
chk "reboot: a bound pane is rebooted WITH its token" \
    "grep -q '^rebooted' '$T/out/rb' && [[ \$(grep -cxF '$TOKEN' '$T/out/tokens') -eq 1 ]]"
: > "$T/out/tokens"; before=$(starts)
"${ENVV[@]}" "$CK/cockpit-reboot" "$pb" >"$T/out/rb2" 2>&1
sleep 1
chk "reboot: a pane whose account no longer resolves is SKIPPED, not rebooted on the primary" \
    "grep -q \"bound to Claude account 'gone'\" '$T/out/rb2' && [[ \$(starts) -eq $before ]]"

echo
if [[ $fail == 0 ]]; then echo "pane-account: ALL CHECKS PASSED"; else echo "pane-account: FAILURES"; fi
exit $fail
