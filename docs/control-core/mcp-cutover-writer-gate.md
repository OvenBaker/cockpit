# MCP cutover-readiness writer gate packet

**Scope:** reversible local readiness only. This branch does not start a live
Cockpit controller, remove/replace wrappers, install a global client config,
or modify Cockpit web or Orbital.

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

## Focused evidence

```text
go test ./internal/core -run 'Test(MCP(Vertical|Nudge|Status|Private|Cutover)|CredentialRegistry)' -count=1 -v
go test ./internal/core -run TestControllerRejectsPublicCredentialRegistry -count=1 -v
bash tests/check-protocol.sh
npx tsc --project tsconfig.json
bash -n scripts/mcp-local-setup.sh
```

The repository-wide Slice 0/1 gates from merged PR #2 are retained as prior
evidence. This branch changes authentication enforcement but not the V1 frame
shape or the tmux ownership model; its focused authority/config plus parity
evidence is included above for this narrow cutover-ready increment.
