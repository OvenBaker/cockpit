# Owner-gated live Cockpit MCP controller

This is the exact reversible controller and wrapper sequence for the explicit
live-Cockpit mode. It is not automatic: the setup helper writes only its named
private root, and neither it nor this document installs a global MCP config or
starts a controller. Retain every legacy wrapper during review and rollback.

Choose one private, dedicated root and keep the controller socket inside it:

```bash
scripts/mcp-local-setup.sh \
  --root /absolute/private/cp-mcp \
  --binary /absolute/path/to/cockpit-core \
  --socket /absolute/private/cp-mcp/control.sock
```

After reviewer GO, the only live controller command is:

```bash
/absolute/path/to/cockpit-core daemon \
  --live-cockpit \
  --runtime-root /absolute/private/cp-mcp \
  --socket /absolute/private/cp-mcp/control.sock \
  --credentials-file /absolute/private/cp-mcp/clients.json
```

The supplied MCP wrapper is:

```bash
/absolute/private/cp-mcp/mcp-run
```

It reads the generated private token and connects to the supplied controller
socket. The helper also emits snippets for manual client configuration; it
does not write them globally. `--live-cockpit` has no general tmux target:
the implementation fixes the target to `tmux -L cockpit`. Normal daemon mode
continues to require a throwaway `--tmux-socket` and refuses `cockpit`.

This PR deliberately does not run either live command. Its admission and
parity proof uses a random isolated tmux server, while preserving the existing
credential, stable identity, fingerprint, CAS, fencing, capability, audit and
redacted driver-trace behavior.
