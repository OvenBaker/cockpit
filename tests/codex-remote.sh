#!/usr/bin/env bash
# A failed backend must not destroy the terminal we are trying to migrate.
set -euo pipefail
CK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
socket="ck-codex-remote-$$"
export COCKPIT_TMUX="tmux -L $socket" COCKPIT_CODEX_REMOTE=1
export COCKPIT_CODEX_REMOTE_URL=ws://127.0.0.1:43129
source "$CK/lib.sh"
trap 'tmux -L "$socket" kill-server 2>/dev/null || true' EXIT
pane=$(tmux -L "$socket" new-session -d -P -F '#{pane_id}' -s test 'sleep 300')
tmux -L "$socket" set -p -t "$pane" @agent codex
tmux -L "$socket" set -p -t "$pane" @session_id test-session
tmux -L "$socket" set -p -t "$pane" @cwd /tmp
before=$(tmux -L "$socket" display -p -t "$pane" '#{pane_pid}')
curl() { return 7; }
if error=$(cockpit_respawn_resume "$pane" 2>&1); then
  echo 'FAIL: restart accepted an unreachable backend' >&2; exit 1
fi
[[ "$error" == *'backend unavailable'* ]]
[[ "$(tmux -L "$socket" display -p -t "$pane" '#{pane_pid}')" == "$before" ]]
kill -0 "$before"
echo 'PASS: backend failure preserves the existing pane and explains recovery'
[[ "$(cockpit_codex_launch_args)" == *'--remote ws://127.0.0.1:43129'* ]]
COCKPIT_CODEX_REMOTE=0
cockpit_codex_backend_ready
[[ "$(cockpit_codex_launch_args)" != *'--remote'* ]]
[[ "$(cockpit_codex_launch_args)" == *'approvals_reviewer=auto_review'* ]]
echo 'PASS: standalone rollback preserves the permission policy'
