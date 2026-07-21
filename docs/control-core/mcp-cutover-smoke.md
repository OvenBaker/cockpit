# Reversible local MCP cutover smoke

This is a local-only readiness check. It does not replace a wrapper, start a
live Cockpit controller, edit a global MCP configuration, or use `tmux -L
cockpit`.

1. Build `cockpit-core` and choose a disposable directory/socket/tmux socket.
2. Run `scripts/mcp-local-setup.sh --root /tmp/cp-mcp-local --binary /absolute/cockpit-core --socket /tmp/cp-mcp-local/control.sock`.
3. Start only a throwaway controller with the emitted `clients.json`; use the
   emitted `mcp-run` in either generated client snippet.
4. Run `go test ./internal/core -run TestMCPCutoverParitySmoke -count=1 -v`.

The transcript covers controller/stdio parity for `list_panes`, `get_status`,
`capture_pane`, and `wait_for_state`, followed by one typed `nudge`. It is
safe to remove the local setup with the same command plus `--remove`; existing
Cockpit wrappers remain untouched.

Interaction text is redacted in the controller driver trace as
`interaction.nudge text=[REDACTED]`; the trace never records the literal
nudge/resume payload.

For the separately owner-gated live controller command, see
[mcp-live-cockpit.md](mcp-live-cockpit.md). It is not part of this disposable
smoke.
