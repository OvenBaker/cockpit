#!/usr/bin/env bash
set -euo pipefail
CK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$CK/lib.sh"
source "$CK/picker/new-session.sh"
COCKPIT_REMOTE_CONTROL=0
COCKPIT_CODEX_REMOTE=1
[[ "$(cockpit_new_launch claude /tmp '' auto)" == 'exec claude --permission-mode auto' ]]
[[ "$(cockpit_new_launch claude /tmp '' bypass)" == 'exec claude --dangerously-skip-permissions' ]]
[[ "$(cockpit_new_launch codex /tmp '' auto)" == *'--remote'* ]]
[[ "$(cockpit_new_launch codex /tmp '' bypass)" == 'exec codex --dangerously-bypass-approvals-and-sandbox' ]]
[[ "$(cockpit_new_launch codex /tmp '' auto)" == *'--remote'* ]]
[[ "$(cockpit_new_launch shell /tmp '' bypass)" == 'exec bash -l' ]]
! cockpit_new_launch claude /tmp '' unknown >/dev/null 2>&1
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/current repo/subdir" "$scratch/other"
git -C "$scratch/current repo" init -q
cockpit_recent_cwds() { printf '%s\n' "$scratch/other" "$scratch/current repo" "$scratch/other"; }
mapfile -t rows < <(cockpit_new_directories "$scratch/current repo/subdir")
[[ "${rows[0]}" == "$scratch/current repo"$'\t'* ]]
[[ "${rows[1]}" == "$scratch/current repo/subdir"$'\t'* ]]
[[ "${rows[2]}" == "$scratch/other"$'\t'* ]]
[[ "${rows[3]}" == '+type'$'\t'* && ${#rows[@]} == 4 ]]
echo 'PASS: bypass is per launch, normal Codex remains remote, current repository is first without duplicates'
