# Cockpit control core — design and Slice 0/1 checkpoint

**Status:** design approved; isolated Slice 0/1 implementation independently closed with `PASS`
**Date:** 2026-07-21
**Decision:** adopt a supervised resident `cockpitd` as the sole ordinary tmux mutation authority, behind a versioned typed Unix-socket protocol.

This package contains the clean design checkpoint requested by `control-surface-brief.md` plus the completed, isolated Slice 0/1 implementation checkpoint. The implementation is confined to two uncommitted worktrees inside this workspace, has independent closure with zero blockers/majors, and has never used the live Cockpit tmux socket.

## Recommendation in one page

- Build `cockpitd` and `cockpitctl` as one Go binary with a pure domain package, a private argv-only tmux driver fixed to the Cockpit socket, a per-resource scheduler, provider observers, and a length-framed JSON-RPC 2.0 Unix-socket transport. Keep JSON Schema and generated TypeScript types as the cross-language client contract.
- Hold one nonblocking kernel `flock` for the whole active lifetime. A stale socket or PID file never grants authority. The controller binds a mode-`0600` socket only after it owns the lock and has reconciled.
- Give every logical workspace and pane an opaque Cockpit identity. A `paneRef` survives reindex, move, process respawn, retarget and a logical restore; tmux `%pane_id` is only a current locator. A hard-deleted/recreated pane gets a new `paneRef` even at the same display position.
- Require every mutation to carry typed expectations for every affected pane/workspace/session/projection: stable refs, generation/resource versions and allowlisted material predicates. Recheck the complete set when the request reaches every queue head, immediately before the tmux effect. Never resolve again from a display index.
- Persist a current projection, tombstones, idempotency records, bounded operation state, and a compact mutation journal in SQLite. This is not event sourcing. Reconciliation combines the store, Cockpit tmux options, live tmux IDs/process facts, transcripts/hooks, and incomplete-operation effect probes.
- Keep observed execution state, attention, display metadata, source health and operation state separate. Clients cannot assert `working` or `idle`.
- Publish a bounded event stream and one-shot `wait_for_change`. Waiting takes no model polling, holds no mutation lock, and is cancelled on explicit cancellation, deadline, or connection close.
- Make `cockpitctl`, compatibility scripts, tmux bindings, MCP, web and Orbital capability-scoped clients of the same protocol. Hooks are untrusted event producers, not writers of tmux metadata.
- Preserve a local-only break-glass helper which holds the same lock. A durable fence makes an abandoned break-glass session block automatic controller activation until explicitly closed and reconciled.
- Start from clean Cockpit `main` (`20f75ea`). The current `feat/lean-runner-gate-badge` commit and its uncommitted layout changes are disposable per owner clarification; recommend deleting that branch/worktree only in a later owner-executed cleanup, not during this exercise. Re-express any desired Runner gate signal as a typed observer source.

## Package map

| Artifact | Purpose |
|---|---|
| [01-evidence-inventory.md](01-evidence-inventory.md) | pinned source evidence; every current Cockpit reader/writer and external consumer; migration disposition |
| [02-adr-001-controller-shape.md](02-adr-001-controller-shape.md) | alternatives, decision drivers, selected controller and consequences |
| [03-architecture-and-sequences.md](03-architecture-and-sequences.md) | domain model, identities, component diagram, concurrency and principal command/recovery sequences |
| [04-protocol-v1.md](04-protocol-v1.md) | transport, request semantics, operation catalogue, errors, events, waits and capability profiles |
| [protocol-v1.types.ts](protocol-v1.types.ts) | precise TypeScript wire/domain type draft |
| [05-persistence-and-recovery.md](05-persistence-and-recovery.md) | lease, store, journal, effect probes, reconciliation, retention and break-glass recovery |
| [06-security-and-privacy.md](06-security-and-privacy.md) | threat model and mitigations for local clients, MCP, web, Orbital, hooks, capture and model writes |
| [07-migration-and-rollback.md](07-migration-and-rollback.md) | reversible strangler phases, clean-main order, compatibility and retirement list |
| [08-acceptance-matrix.md](08-acceptance-matrix.md) | false-pass-resistant tests, including all required race/failure sentinels |
| [09-implementation-plan.md](09-implementation-plan.md) | bounded slices, package layout, language/dependencies and exact first vertical slice |
| [10-independent-review.md](10-independent-review.md) | single bounded reviewer record, findings and resolutions |
| [../../SLICE-0-1-EVIDENCE.md](../../SLICE-0-1-EVIDENCE.md) | executable Slice 0/1 gates and false-pass-resistant oracles |
| [11-slice-0-1-implementation-review.md](11-slice-0-1-implementation-review.md) | implementation provenance, initial block, remediation, verification and same-reviewer closure |

## Checkpoint boundary

Slice 0 contract/characterization, real Orderlies controller-domain isolation and the badge-only Slice 1 Go controller spine are implemented. No service unit, live tmux mutation, global configuration change, Orbital change, commit, push, merge, deployment or installation is included. Provider observers, broader mutations and production cutover remain later slices. All runtime proof deliberately uses throwaway tmux sockets and never touches `tmux -L cockpit`.
