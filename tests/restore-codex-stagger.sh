#!/usr/bin/env bash
# End-to-end: a restore keeps layout order, drops duplicate sessions, and starts
# Codex panes in the active workspace before off-screen panes with a bounded gap.
set -uo pipefail
CK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
T=$(mktemp -d "${TMPDIR:-/tmp}/ckcodex.XXXXXX")
export PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex"
export COCKPIT_LAYOUT_DB="$T/layout.db"
export COCKPIT_SESSION=ckcodex COCKPIT_TMUX="tmux -L ckcodex"
export COCKPIT_REMOTE_CONTROL=0 COCKPIT_CODEX_STAGGER_SECS=1
mkdir -p "$T/bin" "$T/codex/2026/08/05" "$T/projects" "$T/work"

cleanup() {
  tmux -L ckcodex kill-server 2>/dev/null || true
  pkill -f "$T" 2>/dev/null || true
  rm -rf "$T"
}
trap cleanup EXIT

# Record the session id and millisecond timestamp at actual Codex process start.
cat > "$T/bin/codex" <<'STUB'
#!/usr/bin/env bash
sid=""
while (( $# )); do
  if [[ "$1" == resume ]]; then sid="${2:-}"; break; fi
  shift
done
printf '%s\t%s\n' "$sid" "$(date +%s%3N)" >> "$CODEX_START_LOG"
exec sleep 600
STUB
chmod +x "$T/bin/codex"
printf 'export PATH=%s/bin:$PATH\nexport CODEX_START_LOG=%s/starts\n' "$T" "$T" > "$T/.bash_profile"
cp "$T/.bash_profile" "$T/.profile"

source "$CK/lib.sh"
A=aaaaaaaa-0000-0000-0000-000000000001
B=bbbbbbbb-0000-0000-0000-000000000002
C=cccccccc-0000-0000-0000-000000000003
for sid in "$A" "$B" "$C"; do
  printf '{"type":"session_meta","payload":{"id":"%s","cwd":"%s"}}\n' "$sid" "$T/work" \
    > "$T/codex/2026/08/05/rollout-test-$sid.jsonl"
done

# The off-screen workspace occurs first in stored tab order. The duplicate A
# entry must not create the final 'duplicate' workspace or launch A twice.
SNAP=$'@active\tactive\n0\toffscreen\t'"$A"$'\t'"$T/work"$'\toffscreen A\tcodex\n1\tactive\t'"$B"$'\t'"$T/work"$'\tactive B\tcodex\n1\tactive\t'"$C"$'\t'"$T/work"$'\tactive C\tcodex\n2\tduplicate\t'"$A"$'\t'"$T/work"$'\tduplicate A\tcodex'
cockpit_layout_save "$SNAP" || { echo "save failed"; exit 1; }

tmux -L ckcodex kill-server 2>/dev/null || true
env HOME="$T" PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex" CODEX_START_LOG="$T/starts" \
    COCKPIT_LAYOUT_DB="$T/layout.db" COCKPIT_SESSION=ckcodex COCKPIT_TMUX="tmux -L ckcodex" \
    COCKPIT_REMOTE_CONTROL=0 COCKPIT_CODEX_STAGGER_SECS=1 COCKPIT_NO_SINGLETON=1 \
    "$CK/cockpit" --restore </dev/null >"$T/out" 2>&1

for _ in {1..60}; do [[ -f "$T/starts" && "$(wc -l < "$T/starts")" == 3 ]] && break; sleep 0.1; done

fail=0
chk(){ if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; fail=1; fi; }
mapfile -t STARTED < <(cut -f1 "$T/starts" 2>/dev/null)
mapfile -t TIMES < <(cut -f2 "$T/starts" 2>/dev/null)
mapfile -t WINDOWS < <(tmux -L ckcodex list-windows -t ckcodex -F '#{window_name}')

chk "active-workspace Codex panes launch first" \
    '[[ "${STARTED[*]}" == "$B $C $A" ]]'
chk "Codex launches are separated by roughly one second" \
    '[[ $((${TIMES[1]:-0}-${TIMES[0]:-0})) -ge 800 && $((${TIMES[2]:-0}-${TIMES[1]:-0})) -ge 800 ]]'
chk "saved workspace order is unchanged" \
    '[[ "${WINDOWS[*]}" == "offscreen active" ]]'
chk "duplicate session is launched only once" \
    '[[ "$(grep -c "^$A" "$T/starts")" == 1 ]]'
chk "duplicate-only workspace is omitted" \
    '! tmux -L ckcodex list-windows -t ckcodex -F "#{window_name}" | grep -qx duplicate'
chk "restore reports the dropped duplicate" \
    'grep -q "ignored 1 duplicate session entry" "$T/out"'

echo; [[ $fail == 0 ]] && echo "codex restore: all checks passed" || echo "codex restore: FAILURES"
exit $fail
