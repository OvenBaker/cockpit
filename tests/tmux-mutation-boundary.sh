#!/usr/bin/env bash
set -euo pipefail
# Legacy Bash is a characterized migration exception in Slice 1; this is the
# complete allowlist. New production Go mutations must use the typed seam.
legacy=(./cockpit)
for path in "${legacy[@]}"; do
  [[ -f "$path" ]] || { echo "missing documented legacy tmux path: $path" >&2; exit 1; }
done
hits=$(rg -l --glob '*.go' --glob '!*_test.go' '"(set-option|set-window-option|set-pane-option|send-keys|kill-pane|kill-window|new-window|split-window|respawn-pane)"' . || true)
if [[ -n "$hits" && "$hits" != "./internal/core/tmux.go" ]]; then
  echo "tmux mutation verb outside private driver:" >&2
  printf '%s\n' "$hits" >&2
  exit 1
fi
