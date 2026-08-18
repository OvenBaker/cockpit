#!/usr/bin/env bash
# Regression: a snapshot row for a pane the controller has not adopted yet —
# workspaceRef present (it is a WINDOW option every pane in the workspace
# inherits), pane-level tuple empty — must NOT be read as a corrupt identity.
# On 2026-08-18 it was: five such rows aborted a 53-pane restore at pane 14 and
# the whole grid fell back to a fresh pick after a WSL reboot.
# Throwaway tmux socket, sandboxed HOME, scratch layout DB. Never touches the
# live cockpit server.
set -uo pipefail
CK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
T=$(mktemp -d "${TMPDIR:-/tmp}/ckunadopt.XXXXXX")
export PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex"
export COCKPIT_LAYOUT_DB="$T/layout.db"
export COCKPIT_SESSION=ckuna COCKPIT_TMUX="tmux -L ckuna"
export COCKPIT_REMOTE_CONTROL=0
mkdir -p "$T/bin" "$T/codex" "$T/projects" "$T/work"

cat > "$T/bin/claude" <<STUB
#!/usr/bin/env bash
exec sleep 600
STUB
chmod +x "$T/bin/claude"
printf 'export PATH=%s/bin:$PATH\n' "$T" > "$T/.bash_profile"
cp "$T/.bash_profile" "$T/.profile"

source "$CK/lib.sh"
mk(){ local pd="$T/projects/$(encode_project_dir "$T/work")"
      mkdir -p "$pd"; printf '{"type":"system","cwd":"%s","sessionId":"%s"}\n' "$T/work" "$1" > "$pd/$1.jsonl"; }
A=aaaaaaaa-0000-0000-0000-000000000001   # adopted: full controller tuple
B=bbbbbbbb-0000-0000-0000-000000000002   # un-adopted: workspaceRef only
C=cccccccc-0000-0000-0000-000000000003   # sits AFTER the un-adopted row
mk "$A"; mk "$B"; mk "$C"

# 'main' holds an adopted pane and — the shape under test — an un-adopted one.
# 'tail' proves the rebuild keeps going past the un-adopted row.
SNAP=$'@active\tmain\n'\
$'0\tmain\t'"$A"$'\t'"$T/work"$'\tadopted\tclaude\tcpw_main\tcpp_main\t2\t5\tidle\n'\
$'0\tmain\t'"$B"$'\t'"$T/work"$'\tunadopted\tclaude\tcpw_main\t\t\t\t\n'\
$'1\ttail\t'"$C"$'\t'"$T/work"$'\ttail\tclaude\tcpw_tail\tcpp_tail\t1\t1\tidle'
cockpit_layout_save "$SNAP" || { echo "save failed"; exit 1; }

tmux -L ckuna kill-server 2>/dev/null
env HOME="$T" PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex" \
    COCKPIT_LAYOUT_DB="$T/layout.db" COCKPIT_SESSION=ckuna COCKPIT_TMUX="tmux -L ckuna" \
    COCKPIT_REMOTE_CONTROL=0 COCKPIT_NO_SINGLETON=1 \
    "$CK/cockpit" --restore </dev/null >"$T/out" 2>&1
sleep 3
echo "--- restore output ---"; grep -v '^$' "$T/out" | head -3
echo "--- resulting grid ---"
tmux -L ckuna list-panes -s -t ckuna -F '  #{window_index} #{window_name} #{pane_id} ref=#{@cockpit_pane_ref} wref=#{@cockpit_workspace_ref} label=#{@label}'

fail=0
chk(){ if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; fail=1; fi; }
chk "restore did not refuse the un-adopted row" \
    '! grep -q "refusing a partial controller identity" "$T/out"'
chk "all three panes rebuilt — the rebuild continued past the un-adopted row" \
    '[[ "$(tmux -L ckuna list-panes -s -t ckuna | wc -l)" == 3 ]]'
chk "the workspace AFTER the un-adopted row exists" \
    'tmux -L ckuna list-windows -t ckuna -F "#{window_name}" | grep -qx tail'
chk "adopted pane keeps its identity" \
    '[[ "$(tmux -L ckuna list-panes -s -t ckuna -f "#{==:#{@label},adopted}" -F "#{@cockpit_pane_ref}:#{@cockpit_pane_generation}:#{@cockpit_pane_version}")" == "cpp_main:2:5" ]]'
chk "un-adopted pane is left unstamped for the controller to adopt" \
    '[[ -z "$(tmux -L ckuna list-panes -s -t ckuna -f "#{==:#{@label},unadopted}" -F "#{@cockpit_pane_ref}#{@cockpit_pane_generation}#{@cockpit_pane_version}")" ]]'
chk "un-adopted pane still inherits its workspace ref" \
    '[[ "$(tmux -L ckuna list-panes -s -t ckuna -f "#{==:#{@label},unadopted}" -F "#{@cockpit_workspace_ref}")" == "cpw_main" ]]'

tmux -L ckuna kill-server 2>/dev/null; pkill -f "$T" 2>/dev/null; rm -rf "$T"
echo; [[ $fail == 0 ]] && echo "e2e: all checks passed" || echo "e2e: FAILURES"
exit $fail
