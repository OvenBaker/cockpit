#!/usr/bin/env bash
# Real isolated tmux; fake Claude. No production panes, credentials or model calls.
set -euo pipefail
CK=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
T=$(mktemp -d /tmp/ck-profile.XXXXXX); socket="ck-profile-$$"
trap 'tmux -L "$socket" kill-server 2>/dev/null || true; rm -rf "$T"' EXIT
mkdir -p "$T/accounts" "$T/beta" "$T/bin" "$T/work" "$T/.claude/projects"
printf '%s\n' "$T/beta" > "$T/accounts/work2.configdir"
printf '%s\n' '{"claudeAiOauth":{"accessToken":"fixture-only"}}' > "$T/beta/.credentials.json"
cat > "$T/bin/claude" <<'EOF'
#!/usr/bin/env bash
python3 -c 'import os,json; print(json.dumps({k:os.getenv(k) for k in ["CLAUDE_CONFIG_DIR","CLAUDE_CODE_OAUTH_TOKEN"]}))' >> "$PROFILE_RESULT"
sleep 60
EOF
chmod +x "$T/bin/claude"
printf 'export PATH=%q/bin:$PATH\n' "$T" > "$T/.bash_profile"
E=(env HOME="$T" PATH="$T/bin:$PATH" COCKPIT_ACCOUNTS_DIR="$T/accounts" COCKPIT_TMUX="tmux -L $socket" COCKPIT_SESSION=profile COCKPIT_NO_SINGLETON=1 COCKPIT_LAYOUT_DB="$T/layout.db" SANTA_DB="$T/santa.db" COCKPIT_CODEX_REMOTE=0 PROFILE_RESULT="$T/results")
"${E[@]}" tmux -L "$socket" new-session -d -s profile 'sleep 60'
# Simulate inherited stale auth in tmux; explicit primary launches must remove it.
tmux -L "$socket" set-environment -t profile CLAUDE_CONFIG_DIR "$T/beta"
tmux -L "$socket" set-environment -t profile CLAUDE_CODE_OAUTH_TOKEN wrong-account
"${E[@]}" "$CK/cockpit-spawn" --cwd "$T/work" --account work2 >/dev/null
"${E[@]}" "$CK/cockpit-spawn" --cwd "$T/work" >/dev/null
for i in $(seq 1 50); do [[ -f "$T/results" && $(wc -l < "$T/results") -eq 2 ]] && break; sleep 0.1; done
python3 - "$T" <<'PY'
import pathlib,json,sys
p=pathlib.Path(sys.argv[1]);rows=[json.loads(x) for x in (p/'results').read_text().splitlines()]
assert len(rows)==2,rows
assert {'CLAUDE_CONFIG_DIR':str(p/'beta'),'CLAUDE_CODE_OAUTH_TOKEN':None} in rows,rows
assert {'CLAUDE_CONFIG_DIR':None,'CLAUDE_CODE_OAUTH_TOKEN':None} in rows,rows
print('PASS: Beta directory reaches child; primary removes inherited Beta directory and token')
PY
