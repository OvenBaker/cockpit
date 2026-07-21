# MCP vertical slice

`cockpit-core mcp-stdio` is a local stdio MCP adapter, not a second tmux
controller. It opens a `mcp-local` session to the resident controller and only
translates tools declared in `internal/core/registry.go`. All identity checks,
capabilities, idempotency, CAS, fences, audit and cancellation stay on the
controller socket.

## Local configuration (not installed by this slice)

Replace the socket placeholder with the controller's local socket. The
controller requires a private `--credentials-file` registry binding each
credential to a profile, optional exact client ID, and a capability subset.
`mcp-stdio` requires `COCKPIT_MCP_CREDENTIAL_FILE`: an absolute, regular
private file (mode `0600` or stricter) containing its credential. It refuses a
missing, public, malformed, or denied credential and never accepts one in argv
or checked-in configuration.

Codex:

```toml
[mcp_servers.cockpit]
command = "/absolute/path/to/cockpit-core"
args = ["mcp-stdio", "--socket", "/absolute/private/control.sock"]
```

Claude Code:

```json
{
  "mcpServers": {
    "cockpit": {
      "command": "/absolute/path/to/cockpit-core",
      "args": ["mcp-stdio", "--socket", "/absolute/private/control.sock"]
    }
  }
}
```

Pre-approve only the read-only tools: `list_panes`, `get_status`,
`capture_pane`, `wait_for_state`, and `get_capabilities`. `nudge`, `pause`,
`compact`, and `resume` are declared as writes (nudge/resume are additionally
open-world because they transmit caller text) and remain review-gated.

`session:window.pane` is accepted only as a convenience `locator`; it is
resolved immediately by the controller to a stamped `paneRef`, and every write
still supplies generation, resource-version and material-state expectations.

## Current interaction evidence

The owner-approved accelerated surface is intentionally honest about the
Slice 0/1 observer gap. A supported Claude/Codex pane must have controller-read
`@cockpit_provider` and `@cockpit_state` stamps. Nudge/resume are delivered as
bounded literal text; pause/compact are fixed actions. Each interaction gets a
new pane version, is serialized through the same queue/CAS path as metadata,
and audits only digest plus byte count. It returns
`effect-delivered-unconfirmed` until a provider observer supplies
operation-specific completion evidence; it never treats elapsed time as
success. Unsupported or stale/wrong-state panes fail before terminal input.

No installer, service configuration, web/Orbital migration, or cutover is
performed by this slice.

For a reversible local-only setup and parity transcript, see
[mcp-cutover-smoke.md](mcp-cutover-smoke.md). The helper creates an isolated
directory only when explicitly run; it does not modify global client settings.
