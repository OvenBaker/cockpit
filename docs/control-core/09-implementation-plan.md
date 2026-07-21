# Bounded implementation plan

## Runtime and dependency decision

Build one Go module/binary in Cockpit. The binary selects a mode by first subcommand or installed symlink:

```text
cockpit daemon
cockpit ctl ...
cockpit hook ...
cockpit mcp-stdio
cockpit break-glass ...
```

The existing top-level `cockpit` Bash launcher remains during migration; name the new binary `cockpit-core` until the final launcher retirement to avoid collision.

Use:

- Go standard library for `net.UnixListener`, 4-byte framing, strict `encoding/json` decode (`DisallowUnknownFields`), `context`, `os/exec` argv invocation, `log/slog`, hashing/HMAC and testing;
- `modernc.org/sqlite` via `database/sql`, pinned in `go.mod/go.sum`, because the adjacent Lean Runner already uses this pure-Go stack locally;
- `golang.org/x/sys/unix` for explicit Linux `flock`, peer credentials and no-follow/open primitives where standard library does not expose them cleanly;
- `github.com/google/uuid` for UUIDv7 refs, already present adjacent to this workspace;
- canonical checked-in JSON Schema plus strict Go structs/domain validation. Do not add a runtime reflection/schema framework initially. A pinned build-time generator/checker may produce TypeScript declarations, but generated diffs and a shared protocol corpus are the acceptance authority.

Do not add a JSON-RPC framework, dependency-injection container, ORM, event-sourcing library, web framework or generic tmux library. The protocol subset and driver surface are intentionally small. The web gateway may add a pinned JOSE library for Access verification; it remains a separate thin client.

## Likely repository layout

```text
go.mod
go.sum
cmd/cockpit-core/main.go
internal/app/                 mode wiring only
internal/domain/              refs, entities, pure projection, preconditions, errors
internal/protocol/            strict wire structs, method registry, capability registry
internal/protocol/schema/     cockpit-control-v1.schema.json + golden corpus
internal/transport/           framing, Unix auth/session, JSON-RPC, cancellation
internal/controller/          admission, commands, reconciliation, lifecycle
internal/scheduler/           sorted resource queues/global barrier
internal/store/               SQLite migrations/repositories/transactions/retention
internal/driver/tmux/         only normal-production tmux mutation package
internal/observer/tmux/       topology/process facts
internal/observer/claude/     hook/transcript/adoption/classification
internal/observer/codex/      rollout/adoption/classification
internal/observer/runner/     optional later typed source
internal/events/              bounded bus, cursor and wait registry
internal/security/            paths/modes/tokens/redaction/audit digests
internal/client/              Go V1 client used by ctl/hook/MCP/break-glass prep
internal/breakglass/          exclusive helper and fixed recovery actions
test/integration/             real throwaway tmux process tests/fake agents
test/protocol/                cross-language request/response/error corpus
web/                          migrated gateway/UI thin client (later slice)
compat/                       temporary script shims and golden argv tests
```

Package rules enforced by tests/static analysis:

- only `internal/driver/tmux` and explicit `internal/breakglass` may construct mutating tmux argv;
- domain imports no store/driver/transport;
- observers publish facts and cannot import the mutating driver;
- clients/MCP/web/Orbital never import driver/controller internals;
- instruction/capture types cannot be serialized into store/audit records.

## Slices

Each slice is one reviewable change set with its own rollback. No slice installs/enables a service until the final operational slice.

### Implementation orchestration contract

Implementation starts only from a fresh isolated Cockpit worktree based on clean current `main`. The lead architect/orchestrator enters that worktree in a dedicated Cockpit workspace, uses Cockpit to spawn exactly one sibling Codex pane in the same workspace, and verifies its footer reports `gpt-5.6-terra` with high reasoning before handing it the bounded slice. Terra High is the implementation writer; the orchestrator and, where warranted, one separate read-only Opus High sibling pane review its evidence. A one-shot subprocess reviewer is not a substitute for this pane-based writer/reviewer workflow.

Slice 0 and Slice 1 are the only initially authorized implementation scope. After their evidence bundle is reviewed, stop. Do not connect the controller to the live `tmux -L cockpit` socket, install/enable `cockpitd`, or begin Slice 2 without a new explicit owner gate.

### Slice 0 — contract and characterization

Deliver V1 schema/corpus, Go/TS type check, capability/error registry, throwaway-tmux harness, current Bash characterization, live-socket guard and tmux driver trace. Move `orderlies-up` to and characterize the selected separate `tmux -L orderlies` socket before state authority.

Gate: static schema/golden-corpus validation, current behavior characterization and M8. P1–P4 are executable daemon/transport gates and therefore begin in Slice 1; Slice 0 supplies their fixtures only.

### Slice 1 — first vertical slice: stable pane badge CAS on throwaway tmux

This is the exact first implementation slice.

Deliver only:

1. `cockpit-core daemon --test-root <tmp> --tmux-socket <random>` with exclusive lock, SQLite v1 migrations, controller epoch/readiness and hard refusal of socket name `cockpit` in test mode;
2. real tmux inventory and deterministic assignment/stamping of `workspaceRef`/`paneRef`, generation/version and server fingerprint;
3. strict framed RPC authentication for two test client profiles;
4. `controller.health`, `state.snapshot`, `pane.inspect`, `capabilities.get`, `operation.get`;
5. one mutation only: `metadata.set_display` with **badge only** (label remains read-only), max 48 sanitized characters;
6. admission/idempotency, one per-pane scheduler, execution-time CAS, prepared effect intent, typed tmux `set-option` driver plan, confirmation probe and compact audit;
7. `events.subscribe` and `wait.for_change` for resource-version/operation-terminal;
8. startup reconciliation for exact stamp matches and an incomplete badge effect;
9. `cockpit-core ctl` commands sufficient to exercise those methods;
10. real cross-process tests for R3–R6 (including R5b), I7, L1–L3, C2–C3, P1–P7 and the badge subset of O11.

Explicitly excluded: provider transcripts/hooks, spawn/resume, input injection, web/MCP/Orbital, production socket, service files, layout restore and break-glass raw actions.

Why this slice: badge mutation is reversible and does not interrupt or transmit model context, yet it proves the hardest common spine—stable identity, lease, store, protocol, capabilities, CAS, serialization, idempotency, event/wait and crash reconciliation—against real tmux. A read-only first slice would leave the core safety claims untested; spawn is too risky before that spine exists.

Exit artifact: one command transcript/test report showing two separate client processes race the same expected version, one wins, crash-after-effect recovers once, same/different idempotency behavior is correct, and a cancelled wait leaves no watcher. All on a random test socket.

### Slice 2 — read-only shadow and full projection

Add provider-independent topology projection, bounded captures/redaction, Claude/Codex pure classifier ports, source health/disagreement, attention latch, recent/recoverable session queries and shadow comparison reports. Run shadow without live writes.

Gate: R7, R9, I1/I7/I9, P5–P10, M1.

### Slice 3 — hook/adoption/state authority and snapshots

Add hook/spool mode, provider observers/adoption, tmux chrome projection, layout SQLite/snapshot/history, full startup reconcile and thin `cockpit-state`. Replace/stop poller behind a feature switch.

Gate: R7, I3/I4/I7–I9, X1/X2, P14, M2 plus rollback drill.

### Slice 4 — spawn and resume family

First generalize the proven pane queue into the sorted all-or-none multi-resource scheduler with immutable lock sets, session uniqueness keys, dynamic-membership release/fail behavior and observer-update sequencing. Then add fixed agent launch wrapper, spawn/resume effect probes/session guards, workspace creation under controller, cold request handling, thin `cockpit-spawn`/`cockpit-send`/Santa compatibility. No generic shell.

Gate: R4–R6/R13, O1/O2, C2 plus dedicated scheduler all-or-none/session-key tests and family rollback. R1/R1b/R2 and opposite-direction move C1 remain Slice 6 gates because they require remove/move; any earlier raw-topology variants are supplemental, not substitutes.

### Slice 5 — recover, retarget and typed interactions

Add recover/retarget first, then nudge/pause/compact/resume/replay and local-only fixed `interaction.continue_process` as separately capability-gated methods with provider/process-specific completion evidence. Add bounded capture if not already production-enabled and MCP tool semantics tests, but do not ship MCP yet.

Gate: R8/R10, O3–O8, private audit P9 and crash/cancel tests per method.

### Slice 6 — pane/workspace layout and durable timers

Add move, soft remove/undo, rename, soft close/undo, navigation focus and only then decide idle parking/swap. Add multi-resource scheduler/global barrier and break-glass prepare/helper lifecycle.

Gate: R1/R1b/R2/R12, I1/I5/I6, C1, L4, O9/O10/O12.

### Slice 7 — thin local/MCP/web surfaces

Migrate all tmux bindings/compatibility CLIs. Add Go stdio MCP translator from the method registry. Replace web script execution with V1, Access JWT verification, CSRF/origin/rate limits and event push.

Gate: R8/R11/R13, P8/P11/P12, cross-client same-error/result conformance and web/MCP rollback drill.

### Slice 8 — Orbital connector

In a separate clean Orbital worktree, add connector/capability negotiation, paneRef provider identity, legacy explicit mapping and event-driven fleet observation; preserve request-before-foreign-write and fail-closed semantics. Steering remains absent.

Gate: P13, M5–M7, crash after foreign success, all existing G15/Runner tests.

### Slice 9 — enforcement and operations

Set full single-writer enforcement, static tmux boundary test, remove duplicate/reaper/poller logic, document/install user service only with owner approval, run soak/lease/break-glass drills and retain narrow shims.

Gate: complete `08-acceptance-matrix.md`, production-readiness review and explicit owner cutover approval.

## Bounded risk register

| Risk | Earliest proving slice | Containment |
|---|---:|---|
| Go port changes classifier semantics | 2 | golden transcript corpus and live read-only shadow before state cutover |
| tmux effect cannot be probed unambiguously | 1 for metadata; 4 for spawn | operation-specific markers/handshake; ambiguous fences, never retry |
| pure-Go SQLite binary/build cost | 0 | tiny store benchmark/build artifact; adjacent repository proves compatibility |
| stable stamp tampering/external writer | 1/2 | store/fingerprint/stamp coherence, external mutation fence |
| compatibility accidentally remains a second writer | 4 onward | instrumented PATH/static rule and R13 on every family |
| long interaction completion differs by provider version | 5 | explicit provider capabilities/evidence; absence remains capability-absent/unconfirmed |
| web origin identity spoof | 7 | verified Access JWT contract tests; old header-only path cannot be tunnel-enabled |
| Orbital legacy identity ambiguity | 8 | explicit unique mapping/confirmation; otherwise read-only recovery-required |
| external `orderlies-up` violates sole writer | 0/3 | selected separate `tmux -L orderlies`; Phase 3 blocked until trace proves isolation |

## Definition of implementation complete

Implementation is not complete when the daemon merely runs. It is complete only after every current writer has a final disposition, all ordinary mutation traces enter the controller, all required sentinels pass across multiple processes on real throwaway tmux, Orbital persists paneRef for new executions, the web origin verifies Access assertions, the break-glass exclusion drill passes, rollback has been exercised, and duplicate logic/reapers/poller have been retired.
