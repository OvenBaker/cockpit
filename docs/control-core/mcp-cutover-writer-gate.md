# MCP cutover-readiness writer gate packet

**Scope:** an explicit, owner-gated live Cockpit admission path plus reversible
local readiness. This branch does not start a live Cockpit controller,
remove/replace wrappers, install a global client config, or modify Cockpit web
or Orbital.

## Reviewer probes

1. Driver trace: `interaction.nudge`/`interaction.resume` records must contain
   only `text=[REDACTED]`; neither literal prompt text nor raw interaction argv
   may appear in a production-capable driver trace or returned driver error.
2. Auth: a `0600` private registry is decoded strictly, binds a credential to
   one profile and explicit capability subset, and may bind an exact client ID.
   Missing/public/malformed registries and profile/client mismatches fail
   closed before MCP tools are advertised.
3. MCP/CLI credentials are read from private files, never defaulted or passed
   as raw credential argv values. The local setup helper stores the generated
   credential only in its private setup root.
4. Local setup is opt-in and reversible (`--remove`); it preserves all legacy
   wrappers and emits no global configuration change.
5. The smoke transcript must exercise list, status, capture, wait and nudge
   through MCP against a disposable tmux server, with read parity against ctl.
6. `daemon --live-cockpit` has no tmux target argument and is hardwired to
   `cockpit`; it requires `--runtime-root`, `--socket`, and the existing
   private registry. Ordinary constructors and `--tmux-socket cockpit` remain
   refused. The live-mode test uses a fresh random equivalent server only.

## Focused evidence

```text
GOCACHE=/tmp/cp-core-go-cache go test ./internal/core -run TestLiveCockpitAdmissionParityOnEquivalentSocket -count=1 -v  PASS
GOCACHE=/tmp/cp-core-go-cache go test ./internal/core -run TestMCPCutoverParitySmoke -count=1 -v  PASS
bash -n scripts/mcp-local-setup.sh
git diff --check
```

The repository-wide Slice 0/1 gates from merged PR #2 are retained as prior
evidence. This branch changes only live Cockpit admission; it leaves the V1
frame shape, credential registry, identity/CAS/fencing, method registry, and
tmux ownership model intact.
