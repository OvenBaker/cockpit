#!/usr/bin/env bash
# End-to-end: does `cockpit --restore` rebuild the grid FROM SQLITE, and does a
# workspace whose session drifted cwd survive?  Throwaway tmux socket, sandboxed
# HOME (so the login shell each pane runs picks up our stub agent instead of the
# real one), scratch layout DB.  Never touches the live cockpit server.
set -uo pipefail
CK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
T=$(mktemp -d "${TMPDIR:-/tmp}/ckrestore.XXXXXX")
export PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex"
export COCKPIT_LAYOUT_DB="$T/layout.db"
export COCKPIT_SESSION=ckrst COCKPIT_TMUX="tmux -L ckrst"
export COCKPIT_REMOTE_CONTROL=0
mkdir -p "$T/bin" "$T/codex" "$T/projects" "$T/work/sub"

# Stub agent mimicking the ONE rule that matters: `claude --resume <id>` only
# resolves an id whose transcript lives under the CWD's project dir.
cat > "$T/bin/claude" <<STUB
#!/usr/bin/env bash
id=""; prev=""; for a in "\$@"; do [[ "\$prev" == --resume ]] && id="\$a"; prev="\$a"; done
enc=\$(echo "\$PWD" | sed 's#[/.]#-#g')
[[ -f "$T/projects/\$enc/\$id.jsonl" ]] || { echo "No conversation found with session ID: \$id"; exit 1; }
exec sleep 600
STUB
chmod +x "$T/bin/claude"
# Each restored pane launches via `bash -lc`, a LOGIN shell that rebuilds PATH
# from profile — so the stub has to be installed through the sandboxed profile.
printf 'export PATH=%s/bin:$PATH\n' "$T" > "$T/.bash_profile"
cp "$T/.bash_profile" "$T/.profile"

source "$CK/lib.sh"
mk(){ local sid="$1" home="$2"; local pd="$T/projects/$(encode_project_dir "$home")"
      mkdir -p "$pd"; printf '{"type":"system","cwd":"%s","sessionId":"%s"}\n' "$home" "$sid" > "$pd/$sid.jsonl"; }
A=aaaaaaaa-0000-0000-0000-000000000001   # drifted: transcript in $T/work, @cwd $T/work/sub
B=bbbbbbbb-0000-0000-0000-000000000002   # healthy
mk "$A" "$T/work"; mk "$B" "$T/work"

# The layout as the poller would have saved it: 'orbital' is a single-pane
# workspace whose @cwd drifted into a subdirectory — the exact fatal shape.
SNAP=$'@active\tmain\n0\tmain\t'"$B"$'\t'"$T/work"$'\thealthy\tclaude\n1\torbital\t'"$A"$'\t'"$T/work/sub"$'\tdrifted\tclaude'
cockpit_layout_save "$SNAP" || { echo "save failed"; exit 1; }

tmux -L ckrst kill-server 2>/dev/null
env HOME="$T" PROJECTS_DIR="$T/projects" CODEX_SESSIONS="$T/codex" \
    COCKPIT_LAYOUT_DB="$T/layout.db" COCKPIT_SESSION=ckrst COCKPIT_TMUX="tmux -L ckrst" \
    COCKPIT_REMOTE_CONTROL=0 COCKPIT_NO_SINGLETON=1 \
    "$CK/cockpit" --restore </dev/null >"$T/out" 2>&1
sleep 4
echo "--- restore output ---"; grep -v '^$' "$T/out" | head -3
echo "--- resulting grid ---"
tmux -L ckrst list-panes -s -t ckrst -F '  #{window_index} #{window_name} #{pane_id} cmd=#{pane_current_command} cwd=#{pane_current_path}'

fail=0
chk(){ if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; fail=1; fi; }
chk "drifted single-pane workspace survived the restore" \
    'tmux -L ckrst list-windows -t ckrst -F "#{window_name}" | grep -qx orbital'
chk "both panes restored" \
    '[[ "$(tmux -L ckrst list-panes -s -t ckrst | wc -l)" == 2 ]]'
# The keep-alive wrapper means the pane's own process is the shell and the agent
# is its child, so assert on descendants rather than pane_current_command.
agents=0
for pp in $(tmux -L ckrst list-panes -s -t ckrst -F '#{pane_pid}'); do
  pgrep -P "$pp" -a 2>/dev/null | grep -q sleep && agents=$((agents+1))
done
chk "stub agent resumed in BOTH panes (fix 1 worked, not just fix 2)" '[[ "$agents" == 2 ]]'
chk "drifted pane runs in the transcript-owning dir, not the recorded cwd" \
    '[[ "$(tmux -L ckrst list-panes -s -t ckrst -f "#{==:#{window_name},orbital}" -F "#{pane_current_path}")" == "$T/work" ]]'

tmux -L ckrst kill-server 2>/dev/null; pkill -f "$T" 2>/dev/null; rm -rf "$T"
echo; [[ $fail == 0 ]] && echo "e2e: all checks passed" || echo "e2e: FAILURES"
exit $fail
