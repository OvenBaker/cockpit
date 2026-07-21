#!/usr/bin/env bash
set -euo pipefail

root=$(mktemp -d)
socket="cp-it-bash-$RANDOM"
trap 'tmux -L "$socket" kill-server 2>/dev/null || true; rm -rf "$root"' EXIT
TMUX='' tmux -L "$socket" new-session -d -s cockpit -n work 'sleep 60'
TMUX='' tmux -L "$socket" set-option -p -t cockpit:0.0 @session_id session-characterization
TMUX='' tmux -L "$socket" set-option -p -t cockpit:0.0 @cwd "$root"
TMUX='' tmux -L "$socket" set-option -p -t cockpit:0.0 @label 'characterization label'

snapshot=$(COCKPIT_TMUX="tmux -L $socket" COCKPIT_SESSION=cockpit bash -c 'source ./lib.sh; cockpit_snapshot')
current=$(COCKPIT_TMUX="tmux -L $socket" COCKPIT_SESSION=cockpit bash -c 'source ./lib.sh; cockpit_cur_window')
[[ "$snapshot" == *$'@active\twork'* ]]
[[ "$snapshot" == *'session-characterization'* ]]
[[ "$current" == @* ]]
! [[ "$snapshot$current" == *'cockpit'* && "$socket" == cockpit ]]
