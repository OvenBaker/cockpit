# ADR-001 — one resident controller for Cockpit

**Status:** Accepted for implementation planning
**Date:** 2026-07-21
**Owner:** Gareth
**Scope:** ordinary Cockpit control, observation, persistence and client protocol

## Context

Cockpit currently has a singleton state poller but many independent writers. The required property is stronger than “usually one poller”: target resolution, precondition validation, queueing and mutation must share one authority across shell keys, MCP, web, hooks and Orbital. Long operations and waits must survive client disconnects, and a restart must distinguish “effect absent” from “effect happened before the reply was lost.”

## Decision drivers

1. One ordinary mutation authority across concurrent processes.
2. Stable identity independent of tmux indices/IDs.
3. Atomic controller-side CAS immediately before an effect.
4. Cheap push events and cancellable waits.
5. Recoverable in-flight operations without duplicate effects.
6. Thin capability-scoped local and remote-facing clients.
7. Incremental adoption over Bash Cockpit.
8. Small operational and persistence burden for one local operator.

## Considered shapes

### A. Supervised resident `cockpitd` over a Unix socket — selected shape

One process holds an exclusive kernel lock, owns the canonical projection and queues, drives only `tmux -L cockpit`, persists compact state, and serves all clients. Hooks enqueue observations. systemd user supervision is an eventual deployment mechanism, not required for the first slices.

Strengths:

- a single in-memory scheduler can revalidate CAS at execution time;
- sticky attention, source health, timers, operation state and subscriptions have one owner;
- event/wait is naturally push-based;
- incomplete effects can be reconciled before the socket becomes ready;
- client role enforcement and one protocol are centralized;
- the poller’s existing resident lifecycle makes migration evolutionary.

Costs/risks:

- a resident service is a new operational dependency and single availability point;
- controller bugs can fence otherwise usable tmux sessions;
- Go/Bash/tmux boundaries require rigorous integration tests;
- same-UID processes are not a strong hostile isolation boundary.

Mitigations are supervision, fail-closed readiness, compact durable state, a shared-lock break-glass path, and retaining normal tmux attach/read access while controller mutation is unavailable.

### B. Reusable in-process library plus per-operation file/SQLite locks — rejected

Each CLI/web/MCP/Orbital process imports or invokes a common library. A global lock protects a short read-check-mutate section; SQLite stores versions and idempotency. This is credible: it removes duplicated validation and can serialize individual writes without a continuously running daemon.

Why it loses:

- it has no natural owner for subscriptions, long operation state, durable timers, sticky `just-finished`, adoption or source freshness;
- a process holding a wait either polls or becomes an accidental daemon;
- locks serialize calls but do not provide one reconciler after a process dies between tmux effect and store update;
- language boundaries reappear because Bash, Node web/MCP and Orbital cannot literally share one in-process instance;
- each caller still needs tmux credentials, expanding the mutation boundary;
- library/version skew can make concurrent callers classify or validate differently.

It could satisfy “only one effect at a time,” but not “one controller” as a continuous authority without rebuilding a daemon through SQLite queues and workers.

### C. tmux-native command queue and hooks as the authority — rejected

All clients submit tmux commands or set options; tmux’s server serializes them and hooks project state.

Why it loses:

- tmux targets remain ephemeral and its command language is too close to arbitrary terminal automation;
- it lacks typed caller identity, idempotency intent digests, capability profiles and structured errors;
- multi-resource preconditions and durable operation recovery would be encoded indirectly in options/hooks;
- web/MCP privacy and bounded capture policies would still require an external authority.

Tmux remains the layout/process substrate and serial command executor beneath a private driver, not the public protocol.

## Runtime comparison inside the selected shape

The resident shape does not itself dictate a language. Two viable implementations were assessed against the local environment and migration seams.

| Driver | Go single binary | Node/TypeScript service |
|---|---|---|
| Process/deployment | one versioned binary can expose `cockpitd`, `cockpitctl` and break-glass subcommands; no runtime/NVM bootstrap | already familiar to `cockpit-webd` and Orbital, but service units must pin/load a Node runtime |
| Concurrency/cancellation | goroutines, channels and `context.Context` directly fit resource queues, waits and subprocess cancellation | event loop plus async primitives are capable, but per-resource schedulers and cancellation need more application scaffolding |
| Unix boundary | direct Unix listener, peer credentials and `flock`; small syscall surface | all are available, but peer credentials/locking usually require native or less direct bindings |
| SQLite | `database/sql` with pure-Go `modernc.org/sqlite`; this exact stack is already used by adjacent Lean Runner | local `node:sqlite` is flagged experimental; mature alternatives add a native addon or another runtime dependency |
| Protocol types | canonical JSON Schema generates Go validation/types and TypeScript client types | TypeScript wire ergonomics are strongest, but that benefit belongs mainly in clients, not necessarily the daemon |
| Binary/compile footprint | larger compiled binary because pure-Go SQLite, longer build, less dynamic iteration | smaller source iteration loop, but deployed dependency tree/runtime is larger |
| Existing Cockpit reuse | Bash logic must be ported behind characterization tests | webd code style is closer, though little of its orchestration is safe to reuse directly |
| Failure surface | no shell/runtime package lookup after build; strong fit for a long-lived local authority | dynamic runtime/dependencies add more service-start variance |

**Runtime decision:** implement the controller, CLI and break-glass helper as one Go binary. The tighter Unix/process boundary, native concurrency/cancellation model, and locally proven pure-Go SQLite stack outweigh TypeScript’s faster client ergonomics. Keep the web gateway and Orbital connector in their natural TypeScript/JavaScript codebases, generated from the same JSON Schema. This preserves thin-client ergonomics without putting the authority in the broader runtime.

## Decision

Select A as a Go binary: a supervised resident `cockpitd` with these internal boundaries:

1. `domain`: pure identities, schemas, state/attention projection, preconditions, capabilities and errors.
2. `observers`: tmux/process, Claude hooks/transcripts, Codex rollouts and optional Runner facts, each with source health/freshness.
3. `driver-tmux`: private argv-array command builders fixed to the dedicated socket; no generic command method.
4. `runtime`: lease, reconciliation, resource scheduler, operations, timers, idempotency, persistence and event bus.
5. `transport`: length-framed JSON-RPC 2.0 on a mode-`0600` Unix socket with connection-bound authentication and cancellation.
6. thin clients: `cockpitctl`, compatibility shims, stdio MCP, web gateway and Orbital connector.

## Key decisions

- **Runtime/language:** Go, producing one binary with `daemon`, `ctl`, `hook`, `mcp-stdio` and `break-glass` entry modes. The installed toolchain is Go 1.26.1 on Linux/amd64. Bash remains only in temporary compatibility launchers. Web and Orbital remain thin JS/TS clients.
- **Store:** SQLite through `database/sql` and pinned `modernc.org/sqlite`, matching the adjacent Lean Runner’s existing local stack and avoiding CGO/runtime installation coupling. Use WAL, foreign keys, a bounded busy timeout, one writer connection and explicit transactions.
- **Schema:** JSON Schema 2020-12 is canonical. Generate/check Go wire structs and TypeScript client types from it; retain explicit domain validation in Go so schema validation is not mistaken for authorization or state-machine validation.
- **Transport:** 4-byte big-endian length followed by one UTF-8 JSON-RPC message. This is simpler than embedding HTTP, supports full-duplex notifications and cancellation, and is easy for Go, Node and temporary compatibility clients. Maximum frame is 1 MiB.
- **Lease:** advisory `flock` on a mode-`0600` lock file, held by an open file descriptor for the active lifetime, plus a durable fence state. PID and socket files are diagnostic only.
- **Authority boundary:** during normal operation, only `driver-tmux` may mutate the Cockpit server. No public `exec`, argv, tmux command, keypress or `send-keys` method exists.

## Consequences

Positive:

- all clients receive the same state/capabilities/result semantics;
- stale display-index requests fail before effect;
- waits become cheap and approval-friendly;
- Orbital can persist a real Cockpit identity rather than `%pane_id` + label;
- recovery and break-glass are explicit system states.

Negative:

- controller availability becomes required for ordinary mutation;
- compatibility overlap must be carefully fenced so “old plus new” does not mean two writers;
- SQLite and a schema validator add dependencies to a previously mostly shell repository;
- direct same-user tmux commands cannot be prevented cryptographically; they are out-of-contract external mutations detected and reconciled.

## Rejected shortcuts

- Do not promote the current poller PID scan to a lease.
- Do not use workspace/pane index, tmux `%pane_id`, display name, label or agent session ID as Cockpit identity.
- Do not make the MCP, web gateway or Orbital adapter another tmux implementation.
- Do not persist every observation/read as an event stream.
- Do not reuse `claude-pane.sh` unchanged inside the daemon.
- Do not build on the disposable feature branch; start implementation from clean main.
