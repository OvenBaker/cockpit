# Cockpit Core — one controller, many thin control surfaces

**Status:** design exercise brief
**Owner:** Gareth
**Workspace:** `cp-core`
**Date:** 2026-07-21
**Primary repository under study:** `/home/gareth/repos/cockpit`
**Related consumers:** local Cockpit scripts and tmux bindings, proposed local Cockpit MCP, Cockpit web UI, Orbital, `manage-claude-sessions`

## 1. Narrative

Cockpit has grown several useful control surfaces around one underlying tmux grid:

- interactive local shell scripts and tmux key bindings;
- a background poller that derives state, adopts agent sessions, paints metadata, persists layout, and notices attention transitions;
- a loopback web daemon that invokes selected scripts for the phone UI;
- an external `claude-pane.sh` helper used by Codex to inspect and steer panes;
- a proposed local MCP server to replace repeated opaque shell approvals with typed operations;
- Orbital, which observes Cockpit state and invokes its supported spawn/recover entry points, but cannot yet steer an execution.

These surfaces increasingly duplicate target resolution, state classification, validation, concurrency checks, tmux mutation, status rendering, and recovery behavior. They can race because there is no single serialized command authority. The Choir pane-index incident is a concrete example: a caller resolved `cockpit:2.1`, another actor removed a pane, tmux renumbered the remaining panes, and a later mutation hit the wrong session.

The intended outcome is **one resident Cockpit controller and stable common core**. It alone owns ordinary tmux mutations, agent-session adoption, authoritative state projection, per-pane command serialization, layout persistence, and the typed control protocol. Local CLI scripts, tmux bindings, MCP, the web UI, and Orbital become thin capability-scoped clients. Cockpit remains the execution/session authority; Orbital remains a portfolio and routing controller, not an alternative tmux implementation.

This should be delivered as a strangler migration over the working Cockpit, not a wholesale rewrite or a flag day.

## 2. Operator outcomes

1. A pane has one stable Cockpit identity even when tmux indices change, the pane is moved, or its agent process is respawned.
2. Every caller sees the same workspaces, panes, session identities, derived state, attention state, metadata, and operation capabilities.
3. Mutations are serialized per target and rejected on stale expectations rather than acting on a newly renumbered or replaced pane.
4. A local keyboard action, MCP request, phone action, and Orbital command invoke the same typed operation and receive the same result semantics.
5. Waiting for a state transition is event-driven and cheap; agents do not repeatedly spend approval latency or tokens saying “still watching.”
6. Unsupported operations remain capability-absent. No wrapper exposes arbitrary shell, raw `send-keys`, or untyped tmux commands.
7. Cockpit can restart and reconcile its projection from tmux plus durable state without losing agent/session correlation or issuing duplicate commands.
8. There remains an explicit, documented break-glass recovery path when the controller is unavailable, without allowing two normal controllers.

## 3. Current evidence and constraints

### Existing Cockpit

- `/home/gareth/repos/cockpit` is a Bash-heavy tmux control surface with a Node `cockpit-webd`.
- Cockpit runs on the dedicated `tmux -L cockpit` socket.
- Claude and Codex sessions are tracked together via pane metadata such as `@agent`, `@session_id`, `@cwd`, `@label`, `@state`, `@badge`, `@hook_state`, and `@born`.
- `cockpit-poller` is already a singleton and is the closest thing to a controller. It adopts new transcripts, combines hooks with transcript fallback, derives `working | just-finished | needs-input | waiting-gate | idle`, updates pane/workspace metadata, and writes the durable layout snapshot.
- Many scripts still mutate tmux independently: spawn, send/resume, reboot, retarget, add/remove/move, hooks, launcher restore, and UI bindings.
- `cockpit-state` separately reconstructs JSON from tmux options.
- `cockpit-webd` validates HTTP input but shells out with `execFile` to `cockpit-state`, `cockpit-spawn`, `cockpit-reboot`, `cockpit-search`, and `cockpit-send`.
- The current repository is dirty on `feat/lean-runner-gate-badge`. The owner has confirmed that the lean-runner-related changes are disposable and the eventual migration may start from clean `main`; the design exercise itself must still leave the live worktree untouched.

### External helper

`/home/gareth/.codex/skills/manage-claude-sessions/scripts/claude-pane.sh` is useful policy scaffolding but not a suitable core unchanged. It accepts general tmux targets, treats a `✳` title prefix as the idle classifier, uses fixed sleeps/polling, calls raw `send-keys`, and returns raw terminal output. Its behavior has already diverged from Codex panes.

### Orbital

Orbital observes Cockpit through its fleet adapter and invokes tool-owned entry points. G15 supports `cockpit.spawn` and `cockpit.recover`; steering honestly remains capability-absent because Cockpit publishes no supported headless steer operation. Orbital must consume a Cockpit-owned typed contract rather than type into tmux or reimplement session logic.

### Audit signal

Cockpit operations appeared in 953 reviewed requests over the audited window, adding roughly 71 minutes of cumulative review latency. A typed MCP would remove much of that routine overhead, but it should be one client of the controller rather than another independent Cockpit implementation.

## 4. Scope

### In scope

- Domain model for controller, workspace, pane, agent session, observed state, attention state, display metadata, capability, operation, and result.
- Stable Cockpit identities and explicit mapping to ephemeral tmux window/pane IDs and adopted Claude/Codex session IDs.
- A single-active-controller lifecycle with crash detection, restart, reconciliation, and takeover rules.
- Private tmux driver owned only by the controller during normal operation.
- Pure state classification and projection shared across local, web, MCP, and Orbital clients.
- Versioned command/query protocol with schemas, idempotency, expected-version compare-and-set, deadlines, cancellation, and structured errors.
- Per-pane mutation queues plus clear rules for workspace/global operations.
- Event/change stream supporting UI refresh, Orbital observation, and bounded `wait_for_state` without model polling.
- Client capability profiles and authentication/authorization boundaries.
- Thin local CLI/tmux-binding client, local stdio MCP, web gateway/UI client, and Orbital adapter.
- Minimal durable state, mutation journal, layout snapshot, permissions, retention, and recovery.
- Incremental migration and compatibility plan for all existing scripts and the current web service.
- Test strategy including real throwaway tmux integration and cross-client race sentinels.

### Out of scope for this exercise

- Rebuilding Orbital’s portfolio, briefing, Digest, Choir, or execution domains.
- Replacing tmux as Cockpit’s process/layout substrate.
- Multi-user collaboration, team RBAC, or general remote shell execution.
- A generic terminal automation API.
- Automatically sending arbitrary model prompts based on observed output.
- Full implementation, production cutover, merge, push, or deployment during the design exercise.
- An exhaustive audit/evidence graph. Preserve only the bounded operational records required for idempotency, recovery, safety, and diagnosis.

## 5. Architectural hypothesis to challenge

Treat this as a starting hypothesis, not a foregone conclusion.

### `cockpitd`: sole resident controller

A supervised user service owns a Unix-domain control socket and obtains an exclusive controller lease. During normal operation, only this process may mutate the dedicated Cockpit tmux server. It subsumes the current poller’s adoption/state/layout responsibilities and exposes a versioned protocol.

Internally separate:

1. **Pure domain core** — schemas, identities, commands, transitions, capabilities, state classification, target preconditions, idempotency, and error taxonomy.
2. **Agent observers** — Claude hooks/transcripts, Codex transcripts, Runner gate badges, and process/tmux observations.
3. **Private tmux driver** — argv-array operations only; no shell command surface; dedicated socket hard-coded or allowlisted.
4. **Controller runtime** — reconcile loop, per-target command queues, event publication, deadlines/cancellation, lease, durable snapshot/journal, and bounded capture/redaction.
5. **Typed local transport** — likely framed JSON-RPC or HTTP over a Unix socket. Select one based on simplicity, streaming/cancellation support, schema generation, and thin-client ergonomics.

### Thin clients

- **`cockpitctl` / compatibility scripts:** local typed client; old commands initially translate argv to protocol calls. Tmux key bindings call this client.
- **Cockpit MCP:** local stdio server translating MCP tools to the same protocol. It owns no tmux logic. Read tools accurately advertise read-only annotations; mutations remain writes. It returns structured data and bounded, sanitized capture content.
- **Web gateway/UI:** loopback/Access boundary authenticates the operator and calls the controller as a restricted client. It does not shell out to scripts. Push updates use the controller event stream.
- **Orbital adapter:** a restricted service client using stable execution/pane identity and Cockpit-owned typed operations. Orbital can observe all allowed state, spawn/recover, and later steer only if Cockpit advertises that capability. It never receives arbitrary terminal execution.
- **Hooks:** producer events enter through a tiny, nonblocking controller client or safe spool. Hooks must not become a second controller and must tolerate controller restart.

The exercise must compare this with at least one credible alternative, such as a reusable in-process library with no daemon, and explain why it does or does not satisfy “one controller” across concurrent processes and remote clients.

## 6. Identity and concurrency requirements

1. Human-readable `session:window.pane` is display/navigation only; it is not sufficient mutation identity.
2. Controller issues a stable opaque `paneRef` and `workspaceRef`. Define their lifecycle across move, reindex, respawn, retarget, removal, Cockpit restart, and full tmux-server rebuild.
3. Every mutation names the stable target plus an expected version/generation and expected material state. Target resolution, precondition check, queue admission, and mutation must be atomic from the controller’s perspective.
4. Multiple controller processes must not become active. Use a socket/lease/lock design that is safe across crashes and stale files.
5. Every mutating request has caller identity, capability profile, request/idempotency key, deadline, and typed payload. Same key/same intent replays; same key/different intent conflicts.
6. Serialize mutations per pane. Define ordering for operations that touch multiple panes or workspaces and prevent deadlock.
7. A removed/replaced pane, changed agent session, or stale generation returns a structured conflict and never redirects to the pane now occupying the old index.
8. Long-running operations such as compaction expose accepted/running/completed/failed state and can be observed without holding an unsafe raw terminal transaction.

## 7. State and operation model

Keep separate:

- **observed execution state:** derived from hooks, transcript, process and provider facts;
- **attention state:** operator-facing projection such as needs-input or just-finished;
- **declared display metadata:** label/badge set by trusted clients;
- **operation state:** queued/running/completed/failed/cancelled;
- **source health/freshness:** whether observation inputs are current.

Do not allow a client to assert that an agent is “working” or “idle.” A harmless pre-approved metadata operation should be named `set_pane_badge` or `set_display_metadata`, not `set_pane_state`.

Design the smallest complete initial protocol. At minimum assess:

- queries: list/inspect workspaces and panes, bounded sanitized capture, capabilities, operation status, recent/recoverable sessions;
- waits/streams: wait for target/version/state change and subscribe to controller events;
- lifecycle: spawn, resume existing session, retarget, move, soft-remove/undo, recover/reboot;
- interaction: nudge, pause, compact, resume/continue, explicit replay where applicable;
- metadata/navigation: rename workspace, label/badge, select/focus where a local interactive client is permitted;
- maintenance: snapshot/reconcile, health, explicit break-glass preparation.

For each operation specify callers allowed, preconditions, idempotency, effect, completion evidence, timeout/cancellation behavior, audit payload, and whether private context can cross to an external model.

There must be no `exec`, arbitrary argv, raw tmux command, generic keypress, or unrestricted `send-keys` operation.

## 8. Security and privacy

- Dedicated Cockpit tmux socket only; reject all other sessions/sockets.
- Unix socket permissions and durable files default to user-only access.
- The web gateway retains Cloudflare Access/Tunnel and origin identity verification; remote clients never reach the raw Unix socket.
- Client roles/capabilities are least privilege. Orbital should not inherit local break-glass or arbitrary capture rights merely because the web operator has them.
- Captures are line/byte bounded, ANSI/control stripped, and treated as untrusted private model output. Define optional redaction and whether each caller may receive capture text.
- Mutation audit is compact, rotated, private, and records digests rather than full instructions. Avoid durable read logging if tools must honestly remain read-only.
- Nudge/resume can send private material to an external model; pause interrupts work; compact consumes model capacity and changes recoverable context. These remain accurately classified writes at client approval boundaries.
- Break-glass must be explicit, exclusive, local-only, and leave evidence sufficient for `cockpitd` to reconcile before resuming normal control.

## 9. Compatibility and migration

Produce a phased strangler plan with independently reversible checkpoints. The plan should consider:

1. **Contract and characterization:** freeze current behavior; enumerate every direct tmux writer and consumer; add throwaway-socket characterization tests.
2. **Read-only shadow controller:** derive the canonical projection beside the poller and compare outputs without mutation.
3. **State authority:** move adoption/classification/layout persistence into the controller; make `cockpit-state` a thin query client while existing mutation scripts remain temporarily compatible.
4. **Mutation authority:** route spawn/resume/reboot/retarget/move/remove through the controller one operation family at a time. Detect and reject unauthorized direct writers.
5. **Thin surfaces:** ship `cockpitctl`, MCP, and web gateway over the protocol; migrate tmux bindings.
6. **Orbital:** replace file/script-specific integration with a supported Cockpit connector and capability negotiation. Preserve G15 identity, request-before-foreign-write, reconciliation, and fail-closed semantics.
7. **Retirement:** remove duplicated mutation/state logic and retain narrowly scoped compatibility shims plus break-glass.

Account for the dirty current Cockpit branch in the migration procedure. The owner permits discarding its lean-runner-related work, so the plan may recommend a clean-main implementation worktree while leaving the live worktree untouched during design.

## 10. Acceptance criteria for the design package

The exercise is complete when it produces a reviewable package containing:

1. A concise architecture decision record comparing at least two viable controller shapes and selecting one.
2. Current-state inventory naming every direct tmux reader/writer and every external consumer, with ownership and migration disposition.
3. Component/context diagram and principal command/event sequences for spawn, nudge, pause, compact, resume, recover, and crash reconciliation.
4. Versioned protocol draft with schemas or precise TypeScript types, error taxonomy, capability model, and client profiles.
5. Stable identity and generation semantics covering pane reindex, move, respawn, retarget, kill, controller restart, and tmux rebuild.
6. Concurrency model with per-pane serialization, multi-target ordering, idempotency, atomic precondition checks, and the exact pane-index-race sentinel.
7. Persistence/recovery design that is materially simpler than an event-sourced audit platform.
8. Security/privacy threat model for local MCP, web/Access, Orbital, hooks, captures, and break-glass.
9. Strangler migration plan with rollback at each phase, overlap with current dirty work, and explicit retirement list.
10. False-pass-resistant acceptance matrix and test strategy, including multiple client processes racing on a throwaway tmux socket.
11. A bounded implementation plan with slices, likely file/package layout, language/runtime choice, dependency decision, and named first vertical slice.
12. Independent architecture review findings resolved to a final recommendation. Use a fresh bounded reviewer; do not spend Fable on routine gates.

## 11. Required race and failure sentinels

The acceptance matrix must include at least:

- resolve pane by display index, delete a sibling, then attempt mutation: stale request is rejected and the remaining pane is untouched;
- delete/recreate a pane with the same display position: old `paneRef` and generation cannot target it;
- two clients submit different operations against the same expected version: exactly one wins or a declared safe ordering applies;
- same idempotency key with same/different intent: replay versus conflict;
- controller dies after tmux effect but before response: restart reconciles without duplicating the effect;
- stale hook/transcript disagreement and source-health degradation remain visible;
- MCP wait cancellation leaves no watcher or later mutation;
- oversized/control-character capture and instruction payloads fail safely;
- unauthorized web/Orbital caller cannot capture or mutate outside its capabilities;
- break-glass and normal controller cannot both mutate concurrently;
- legacy compatibility client and new client still converge through the one controller.

## 12. Design process and stop condition

Work read-only against `/home/gareth/repos/cockpit`, Orbital, and the external helper. Create all design artifacts only inside `/home/gareth/workspaces/lean/cp-core`. Do not modify the dirty Cockpit worktree or any live tmux layout except for the `cp-core` workspace created to run this exercise. Do not push, merge, deploy, install services, edit global Codex configuration, or implement the controller.

The orchestrator should inspect actual code rather than rely on this brief’s summary, challenge the architectural hypothesis, produce the complete package, run one bounded independent review, resolve material findings, and stop at a clean design checkpoint with the exact recommended first implementation slice.
