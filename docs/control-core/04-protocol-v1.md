# Cockpit Control Protocol v1

## Protocol and transport

The canonical contract is JSON Schema 2020-12. `protocol-v1.types.ts` fixes the intended V1 types for review; implementation generates/checks Go and TypeScript wire types from the schema.

- Unix stream socket: `$XDG_RUNTIME_DIR/cockpit/control.sock`, directory `0700`, socket `0600`.
- Frame: unsigned 32-bit big-endian byte length followed by exactly one UTF-8 JSON object. Maximum 1 MiB; invalid UTF-8, trailing bytes and oversize length close the connection after a structured error where possible.
- Envelope: JSON-RPC 2.0 request/result/error. Parse/invalid-request/method/params failures use the standard numeric codes; controller domain failures use numeric `-32001` with the stable `CockpitError` string code in `error.data`. Server notifications carry `controller.event`. `rpc.cancel` cancels an outstanding request ID; `operation.cancel` requests durable operation cancellation.
- First request: `session.open` with protocol version, client ID/profile and a credential read from a protected file or inherited descriptor. Linux peer UID must equal the controller UID. The controller binds the authenticated profile to the connection; a caller cannot select a broader role.
- Deadlines are absolute RFC 3339 timestamps. The server clamps them to per-method maxima and rejects expired requests before admission.
- Unknown fields are rejected in mutation payloads and authentication. Query response readers must ignore newly added optional fields within the same minor version.

V1 compatibility is major-version exact. New optional query fields/methods/capabilities may appear in `1.x`; changing effect or precondition semantics requires a new major method/version.

## Request, operation and idempotency semantics

All mutations return an `OperationView`. Fast operations may already be terminal; callers must not assume the RPC connection remains open until completion.

The JSON-RPC envelope `id` is the request identity; it is not duplicated in params. Every mutation carries a non-empty, discriminated `expectations` list. Each pane/workspace expectation names the exact stable ref, generation, resource version and allowlisted material predicates; session expectations express uniqueness; maintenance and break-glass use the controller epoch/projection version. The exact coverage is method-defined:

| Mutation family | Required execution-time expectations |
|---|---|
| pane-local metadata/interaction/recover/remove | exact pane |
| `pane.move` | pane + expected source workspace + destination workspace |
| `pane.spawn` | destination workspace |
| `session.resume` | destination workspace + session `not-live` |
| `pane.retarget` | pane + new session `not-live` |
| workspace close/undo | workspace with exact member digest; scheduler also locks every named member |
| maintenance/break-glass | exact projection epoch/version; scoped maintenance additionally names its target |

Missing, duplicate, unrelated or incorrectly ordered expectations are `INVALID_REQUEST`. Material membership changing between admission and execution releases the **entire** lock claim and returns `QUEUE_CONFLICT`; a client may re-read and submit a new intent. The controller never silently recomputes a lock set while holding part of the old set.

The idempotency namespace is `(authenticated callerId, method, idempotencyKey)`. Keys have the syntax `ik_<unix-seconds>_<128-bit-random>`; the embedded creation time is immutable, may be at most five minutes in the future, and bounds admission to 30 days. Canonical intent includes protocol major, method, stable targets, all expectations and typed payload; it excludes the JSON-RPC envelope ID and deadline. The controller stores an HMAC-SHA-256 intent digest:

- same key and same canonical intent: return the original operation/result, including after restart;
- same key and different intent: `IDEMPOTENCY_CONFLICT`, no effect;
- any key older than 30 days: `IDEMPOTENCY_EXPIRED`, no effect, even if its retained row has already been pruned; a client must make a deliberate new request with a fresh key;
- an operation with uncertain effect is replayed as `recovery-required`, never reissued automatically;
- instruction/capture text is never placed in the audit/journal. The instruction digest participates in intent equality. If replay needs content after a restart, the client resupplies it and the digest must match.

Mutating deadlines govern queue admission and any controller-side waiting. Once a provider-visible effect is delivered, cancellation cannot erase it. Results distinguish cancellation before effect from a cancellation request while an external-model operation continues.

## Query and stream methods

| Method | Required capability | Bounds and result |
|---|---|---|
| `controller.health` | authenticated connection | lease epoch, ready/degraded/fenced state, store/schema version, source summaries; never secrets |
| `capabilities.get` | `state:read` | effective server/client/target capabilities and reasons for absence |
| `state.snapshot` | `state:read` | one committed projection with epoch/event cursor; supports optional exact refs, no arbitrary filter language |
| `workspace.inspect` / `pane.inspect` | `state:read` | exact stable ref only; includes current diagnostic locator and source health |
| `pane.capture` | `capture:sanitized` or `capture:unredacted` | 1–200 tail lines, max 65,536 output bytes; ANSI/C0/C1 stripped, UTF-8 repaired, optional redaction; marks output untrusted/private |
| `sessions.search` / `recent` / `recoverable` | `sessions:read` | max 50 results; provider-qualified IDs; no transcript body |
| `operation.get` / `list` | `operations:read` | caller’s operations by default; local operator may inspect all bounded metadata |
| `attention.next` | `state:read` | pure bounded query for the next visible pane in a requested attention-state set; never focuses or acknowledges it |
| `events.subscribe` | `events:wait` | notifications after cursor; bounded filters by event type and exact refs; ring gap or epoch mismatch gives `resync-required` |
| `events.unsubscribe` | `events:wait` | releases server subscription immediately |
| `wait.for_change` | `events:wait` | one typed predicate, max 30 minutes; snapshot-before-register avoids lost wakeup; returns on match/deadline/cancel/resync |

Capture rules are enforced after capture as well as before: the driver captures at most the requested line window, the sanitizer caps bytes without splitting UTF-8, and the response never includes terminal escape/control sequences. `redaction=none` requires `capture:unredacted`, available only to the local operator. Web and Orbital have no capture capability in V1.

Events contain refs, versions, states, error codes and digests—not instruction text, capture, transcript lines, cwd secrets beyond the client’s normal state grant, or full tmux argv.

## Mutation catalogue

“Local” means local operator/tmux-binding profile; MCP is the local model-facing adapter; Web is the authenticated gateway; Orbital is the restricted service profile. Every row also requires target-advertised capability and execution-time CAS.

| Method | Allowed V1 callers | Preconditions/effect | Completion evidence | Timeout/cancellation | Compact audit; private-context rule |
|---|---|---|---|---|---|
| `pane.spawn` | Local, MCP, Web, Orbital | active destination workspace; existing absolute allowed cwd; provider `claude|codex`; create exactly one typed agent pane and stamps. No shell/arbitrary command | matching `paneRef` stamp + live pane/process fingerprint; session adoption is later | no retry on unconfirmed create; cancel only before effect | provider, cwd digest, workspace, external correlation, result ref; no prompt. Orbital request key becomes Cockpit idempotency key |
| `session.resume` | Local, MCP, Web | provider session exists/recoverable and not live; spawn/reuse into destination | stamped pane process plus expected session binding/launch; later observer health event | unconfirmed launch never auto-resends | provider/session digest, target refs; no transcript text |
| `pane.retarget` | Local, MCP | active/paused exact pane; new provider session not live; replace process/session binding | new process fingerprint and matching adopted/expected session | cancel before respawn only; post-effect timeout is unconfirmed | old/new session digests, generations; no transcript |
| `pane.move` | Local, MCP | pane active in expected source; destination active; sorted pane+workspace locks | live topology/stamps show same pane ref under destination; source/dest versions updated | cancel before join only | refs and before/after workspace; no private context |
| `pane.soft_remove` | Local, MCP | active pane; workspace invariant permits removal | pane in controller graveyard with same ref, durable expiry operation | cancel before move; undo is separate operation | ref, expiry, lifecycle; no content |
| `pane.undo_remove` | Local, MCP | matching recoverable remove operation, grace not expired | same ref active in chosen/original workspace | no auto fallback if workspace changed; CAS conflict | remove op and destination refs |
| `workspace.rename` | Local, MCP | active workspace, unique sanitized name | tmux window and projection agree | cancel before effect | old/new bounded display names |
| `workspace.soft_close` / `undo_close` | Local | not last active workspace; locks all members; typed grace timer | closed workspace isolated or restored with all refs | cancellation/undo explicit; reaper is controller timer | workspace/member refs, expiry; no capture |
| `pane.recover` | Local, MCP, Web, Orbital | exact active pane; supported provider; same bound session; normally waiting/failed unless privileged explicit override | same pane ref, new process fingerprint, expected session resumed, generation/version increment | after respawn, timeout is unconfirmed and never re-respawned automatically | target/session digest, old/new generation, force flag; no prompt |
| `interaction.nudge` | Local, MCP; Web only if separately enabled | verified agent pane and stable waiting state; 1–16,384 UTF-8 bytes; no forbidden controls | new provider turn/activity causally after op start | cancel before delivery; timeout unconfirmed, no resend | HMAC content digest/byte count/provider; text crosses to external model but is not durable |
| `interaction.pause` | Local, MCP; Web if enabled | verified interruptible agent pane; working/waiting expected state | explicit turn-aborted/stopped or provider-specific stable waiting evidence | fixed max 30s; timeout unconfirmed; no repeated interrupts | provider, state transition, evidence digest; interruption is a write |
| `interaction.compact` | Local, MCP | Claude pane, stable waiting, no other interaction op | explicit provider/transcript `Compacted` evidence plus stable waiting | up to 10m; client disconnect does not cancel; cancel distinguishes pre/post delivery | no content; capacity-changing external-model write |
| `interaction.resume` | Local, MCP; Web if enabled | controller-recorded paused/stopped state; bounded private instruction | matching new provider activity after op | same as nudge | content digest/bytes; private text crosses model boundary |
| `interaction.replay` | Local, MCP | prior operation failed/unconfirmed and is explicitly replayable; caller resupplies identical content digest; current CAS still matches | method-specific | never implicit; new operation ref but linked audit | prior op, digest, reason; no raw content |
| `interaction.continue_process` | Local physical operator/tmux binding only | exact pane generation/version and exact current `{pid,startTicks,processGroupId}`; verified stopped provider process; performs one fixed process-group continue, never accepts PID/signal input | post-action liveness plus matching process fingerprint and provider observation | one attempt only; absence is `TIMEOUT_UNCONFIRMED`, never automatic repeat | pane ref, fingerprint digest, before/after state; no arbitrary signal surface |
| `metadata.set_display` | Local, MCP, Web; Orbital only on panes it spawned | exact pane; label max 160 chars, badge max 48; controls stripped/rejected; cannot set observed/attention state | projection and tmux display stamp agree | fast; cancel before effect | old/new bounded metadata, caller; no model transmission |
| `navigation.focus` | Local/tmux binding only | exact pane active and caller has local client TTY fingerprint | selected pane/window reported by tmux | best effort, max 2s | target only; not exposed to remote/model clients |
| `maintenance.snapshot` | Local | global barrier; controller ready | atomic snapshot/checkpoint committed and verified | no cancellation after write begins | counts/digest/path class, never file content |
| `maintenance.reconcile` | Local | global barrier; optional exact ref; controller not in active break-glass | reconciliation report and all affected versions/generations committed | bounded; cancellation before repair effects only | discrepancy/error codes, refs, digests |
| `break_glass.prepare` | Local physical/TTY operator only | global barrier, no active break-glass; drain/fail queued writes | snapshot fsynced, durable fence written, socket stops mutations, daemon exits | not remotely cancellable after fence | operator, epoch, fence nonce digest, counts; no secrets |
| `operation.cancel` | operation owner; local operator for all | operation cancellable under its method/state | terminal cancellation or `effect-continuing` status | method-specific | operation ref and outcome |

Fixed local scratch-shell launch is intentionally absent from V1. Supporting generic shells would turn this into a terminal automation API. Existing `Alt-N -> bash` remains a temporary local compatibility exception and must not be exposed through MCP, web or Orbital.

## Error taxonomy

| Error | Meaning and retry guidance |
|---|---|
| `INVALID_REQUEST`, `UNSUPPORTED_PROTOCOL`, `FRAME_TOO_LARGE` | caller/schema/transport defect; do not retry unchanged |
| `UNAUTHENTICATED`, `PERMISSION_DENIED` | identity/profile failure; no capability details beyond safe reason |
| `CAPABILITY_ABSENT` | operation not supported by server/profile/provider/current target; re-query capabilities after a meaningful state/version change |
| `TARGET_NOT_FOUND` | ref never known or caller cannot see it (same response prevents enumeration) |
| `TARGET_GONE` | authorized caller’s known ref is tombstoned; never redirect |
| `TARGET_QUARANTINED` | live tmux object exists but identity/evidence is unsafe; reconcile/operator action required |
| `CONFLICT_VERSION` | resource changed since client snapshot; fetch exact target and decide again |
| `CONFLICT_GENERATION` | logical binding/incarnation changed; never blind retry |
| `CONFLICT_MATERIAL_STATE` | expected lifecycle/session/state set no longer holds |
| `IDEMPOTENCY_CONFLICT` | same key used for a different canonical intent; choose a new key only for genuinely new intent |
| `IDEMPOTENCY_EXPIRED` | key creation time is outside the 30-day replay horizon; no mutation is admitted even if its row was pruned |
| `SESSION_ALREADY_LIVE` | duplicate resume/retarget prevented; result may include visible existing `paneRef` if authorized |
| `QUEUE_CONFLICT` | lock set/topology changed during acquisition; caller receives fresh versions |
| `DEADLINE_EXCEEDED`, `CANCELLED` | no effect if explicitly marked before-effect; inspect operation otherwise |
| `TIMEOUT_UNCONFIRMED` | effect may have been delivered but completion evidence was absent; never auto-repeat |
| `SOURCE_STALE` | required provider fact is too old/unavailable for a safe effect |
| `CONTROLLER_NOT_READY` | startup reconciliation/fence/degraded condition; reads may still work |
| `EXTERNAL_MUTATION_DETECTED` | direct writer changed controlled tmux state; affected targets are fenced |
| `EFFECT_AMBIGUOUS` | restart/reconcile cannot prove effect absent or exactly once; owner reconciliation required |
| `BREAK_GLASS_ACTIVE` | durable break-glass fence blocks normal mutation |
| `INTERNAL` | bounded correlation ID only; no stack/path/private output returned to restricted clients |

Errors are stable machine codes plus a concise human message. Restricted callers never receive another client’s ID, raw tmux output, filesystem paths outside their allowed state, stack traces or instruction text.

## Capability derivation and client matrix

Effective capabilities are the intersection of:

```text
compiled server method support
∩ authenticated client profile
∩ target/provider support
∩ current lifecycle/source-health preconditions
∩ deployment policy toggle that can only remove, never invent, capability
```

| Capability family | Local operator | Tmux binding | Local MCP | Web gateway | Orbital | Hook producer |
|---|---:|---:|---:|---:|---:|---:|
| state/capabilities/operations read | ✓ | target/current only | ✓ | ✓ sanitized projection | ✓ allowed fleet | — |
| sanitized capture | ✓ | — | ✓ bounded | — | — | — |
| unredacted capture | explicit local flag | — | — | — | — | — |
| events/wait | ✓ | short wait | ✓ | ✓ | ✓ | — |
| spawn new agent | ✓ | picker request | ✓ | ✓ | ✓ with cwd/correlation policy |
| resume existing / retarget | ✓ | picker request | ✓ | resume only | — V1 | — |
| move/remove/undo/workspace layout | ✓ | ✓ named actions | ✓ | — | — | — |
| recover | ✓ | named action | ✓ | ✓ guarded | ✓ owned pane only | — |
| nudge/pause/compact/resume/replay | ✓ | named actions | ✓ writes | optional nudge/pause/resume only; off by default | absent V1 | — |
| continue verified stopped process | ✓ local TTY | named action | — | — | — | — |
| display metadata | ✓ | ✓ | ✓ | ✓ | owned pane badge/label only | — |
| local focus/navigation | ✓ | ✓ | — | — | — | — |
| maintenance snapshot/reconcile | ✓ | — | — | — | — | — |
| break-glass prepare | physical TTY + explicit confirmation | — | — | — | — | — |
| publish observer facts | — | — | — | — | — | ✓ bounded only |

MCP tool annotations must be honest: state/capture/capability/operation/wait tools are read-only; spawn, resume, recover, metadata and every interaction operation are writes. `wait.for_change` is read-only and cannot schedule a later mutation.

## Event and wait correctness

The controller takes a committed snapshot, evaluates the predicate, then registers the watcher against the next event sequence under one bus mutex. This prevents the change-between-check-and-subscribe gap. Watchers are indexed by exact ref/operation and removed on match, deadline, explicit `rpc.cancel`, connection close or controller shutdown. Limits: 64 waits per connection, 256 subscriptions total, 30-minute maximum wait, and bounded event summaries.

The in-memory event ring holds the latest 10,000 summaries or 24 hours, whichever is smaller. `eventSeq` is monotonic within a controller epoch. A restart changes the epoch; a client must fetch `state.snapshot` and resubscribe. Durable operation terminal results do not depend on the ring.

## Hook ingestion contract

`cockpit hook` opens the same `control.sock`, performs `session.open` with the profile-bound `hook-producer` credential, and may call only `hook.publish`. The strict envelope is capped at 64 KiB and contains an event ID, provider/kind/time, volatile pane ID, optional provider-qualified session and transcript-path digest—never transcript content or an arbitrary path to open. It receives only `{accepted,duplicate}`. If the socket is unavailable, the same validated envelope is atomically spooled as described in the recovery model and later enters the identical validation/deduplication path. Hooks cannot use queries, metadata mutations, events or normal operation methods.
