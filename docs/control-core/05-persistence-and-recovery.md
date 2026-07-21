# Persistence, lease and recovery model

## Design goal

Persist only what is needed to keep identity, prevent duplicate mutation, restore layout, reconcile crashes and diagnose bounded recent operations. Live provider output and every read are not an audit platform.

## Files and permissions

| Path | Mode | Purpose |
|---|---:|---|
| `$XDG_RUNTIME_DIR/cockpit/` | `0700` | ephemeral socket, lock and hook spool directory |
| `controller.lock` | `0600` | kernel-held exclusive lease; file contents diagnostic only |
| `control.sock` | `0600` | normal framed RPC |
| `hooks/` | `0700` | bounded atomic spool fallback when socket unavailable |
| `$XDG_STATE_HOME/cockpit/` | `0700` | durable controller database, snapshots and compact audit |
| `controller.db`, `-wal`, `-shm` | user-only | projection/journal/idempotency |
| `layout-current.json` | `0600` | human-inspectable atomic recovery snapshot derived from DB |
| `layout-history/*.json` | `0600` | spaced restore points, bounded retention |
| `break-glass.json` | `0600` | durable fence record, written by atomic rename + fsync |
| `clients/*.token` | `0600` | profile-bound random credentials; never argv/log |

The implementation sets `umask 077` and rejects symlinks, unexpected owner/mode, non-regular durable files and paths escaping the resolved Cockpit state/runtime directories.

## Single-controller lease

1. Open the explicit `controller.lock` path with create/no-follow, verify owner/mode, and attempt `flock(LOCK_EX|LOCK_NB)`.
2. Failure means another controller or break-glass helper owns authority. Exit without unlinking a socket, changing tmux or rewriting diagnostics.
3. While holding the file descriptor, read `break-glass.json`. On an active fence, report the fenced reason locally and exit without binding the normal socket or touching tmux, releasing the lock so the break-glass helper can acquire it. Diagnostics come from `cockpit break-glass status`, which reads the protected fence record; a fenced daemon never lingers as a second authority.
4. If unfenced, remove a stale socket only after verifying it is a socket owned by this UID under the exact runtime directory. Bind with mode `0600`.
5. Generate a controller epoch, open/migrate DB, reconcile store ↔ tmux, then publish `READY`. Socket existence is never readiness.
6. Hold the lock file descriptor until process exit. Kernel close on crash releases the lease; stale file contents/PID do not.

The lock serializes normal daemon and break-glass. The tmux server cannot enforce this against arbitrary same-UID manual commands, so normal scripts are changed to refuse direct write when a ready controller exists, and the controller detects out-of-band topology/stamp changes.

## SQLite model

Use `database/sql` with `modernc.org/sqlite`, one writer connection, WAL, foreign keys, `busy_timeout=5000`, and `synchronous=FULL` for the low-volume control store. Migration DDL and `PRAGMA user_version` change in one transaction. Startup refuses a future schema or failed integrity check; it never deletes/recreates automatically.

| Table | Durable contents |
|---|---|
| `controller_meta` | schema version, last clean shutdown, last server fingerprint, projection version, last event sequence checkpoint |
| `workspaces` | workspace ref, generation/version, lifecycle, display name, current tmux window id/fingerprint, timestamps |
| `panes` | pane ref, generation/version, lifecycle, workspace ref, provider/session binding, cwd, current tmux locator/process fingerprint, display metadata |
| `pane_bindings` | bounded last 10 provider session bindings per pane, reason and time; no transcript |
| `tombstones` | removed refs/generation/version and removal operation; prevents reuse; retained 90 days, then compacted to an ID digest set for another year |
| `operations` | operation ref, caller/profile, method, refs, state/times, intent HMAC, exact expectations list, effect flag, typed result/error summary, evidence digest |
| `effect_intents` | one row per mutation: operation, typed effect kind, before digest, probe fields, phase and recovered outcome |
| `idempotency` | caller/method/key HMAC, immutable key creation time, intent HMAC, operation ref, replay-until |
| `source_health` | latest source status/freshness/error code by source and bound ref; no raw event body |
| `timers` | recoverable soft-remove/close expiry and operation link |
| `audit` | compact mutation/break-glass/security records; no ordinary reads, capture or instruction text |

Observed state/attention may be checkpointed in the pane projection for restart display, but it is marked stale until a live source refreshes. The event ring and active watchers are memory-only.

### Transactions versus tmux effects

SQLite and tmux cannot share an atomic transaction. The controller uses a small write-ahead effect protocol:

1. under all resource locks, transactionally recheck CAS and commit `effect_intent(phase='prepared')` with a before-state digest and operation-specific probe;
2. issue one typed driver plan. Where tmux supports a command list, effect and identity/operation stamps are submitted together;
3. immediately inspect exact targets and compare the probe;
4. transactionally update resource rows, mark the intent `confirmed`, complete/fail the operation, then publish events.

The prepared record is intentionally durable before tmux. It makes the crash window explicit. Recovery never retries a prepared effect until its probe proves the effect absent and the operation’s retry policy explicitly allows a new attempt; default V1 behavior marks absent as failed-before-effect and lets the caller issue a new request.

## Operation-specific effect probes

| Effect | Prepared evidence | Confirmation/recovery probe |
|---|---|---|
| spawn/resume | pre-effect pane ID/stamp set, destination ref/version, allocated paneRef/opRef, provider/cwd/launch digest | find exactly one pane with allocated ref/op stamp; fallback is exactly one new pane whose process environment/wrapper handshake carries the opRef and whose workspace/process facts match. Zero = absent; more/contradiction = ambiguous |
| respawn/recover/retarget | pane ref/stamp, old pane id/process start time, expected old/new session | same pane ref, process fingerprint changed after intent, `@cockpit_last_op` and expected launch/session facts match |
| move/soft-remove/undo | pane ref, source/destination refs and member digests | same pane stamp appears in exactly one expected destination; no duplicate; source membership matches postcondition |
| metadata/rename | resource stamp, old value digest, desired value digest | exact resource has desired sanitized value and op stamp; old value means absent |
| pause/nudge/resume/compact | pane/session/process fingerprint, transcript/hook cursor, instruction/fixed-action digest | input-delivery stamp plus provider observation causally after cursor. If delivery is known but semantic completion absent, `TIMEOUT_UNCONFIRMED`, never absent |
| continue process | exact pane generation/version and `{pid,startTicks,processGroupId}` digest | the same verified process group becomes live and a causally later provider fact arrives; one delivery only |
| delayed hard removal | timer/op, pane/workspace ref and soft-removed lifecycle | tombstone plus target absent; if target was restored/version changed, timer is obsolete and must not kill |

The agent launch wrapper is fixed controller code, not a shell surface. It receives `paneRef/opRef` as controller-generated environment, performs an immediate bounded handshake to the controller or atomic spool, then `exec`s only the allowlisted provider launch. On restart, process environment/handshake is recovery evidence; it is never returned to clients.

Literal interaction input uses a controller-namespaced, operation-specific tmux buffer created by the private driver; the driver submits it with fixed argv and deletes that exact named buffer in a guaranteed cleanup path. Startup reconciliation enumerates and deletes only orphan buffers bearing the valid controller namespace/opRef whose operations are terminal or absent. It never runs `delete-buffer` without the exact controller-generated name and never treats buffer existence as completion evidence.

## Startup reconciliation

Reconciliation runs under the global mutation barrier before `READY`:

1. Verify DB/integrity and load active break-glass state.
2. Query only the dedicated Cockpit tmux server for server fingerprint, sessions/windows/panes, identity stamps, options, pane PID/current command/dead state and topology.
3. Partition live objects into exact matches, missing durable objects, unknown live objects, duplicate/conflicting stamps and server-rebuild candidates.
4. Reconcile every incomplete `effect_intent` using its typed probe.
5. For exact matches, update only volatile locators/freshness; do not bump generation/version merely because controller epoch changed.
6. For expected objects missing without a prepared effect, mark lifecycle `missing`, bump generation/version and expose degraded state; never attach a replacement by index.
7. Unknown live objects enter `quarantined`. Read-only diagnostics may expose them to local operator; normal clients cannot mutate until explicit adopt/remove reconciliation.
8. Conflicting/duplicate stamps fence all implicated refs and emit `EXTERNAL_MUTATION_DETECTED`/`EFFECT_AMBIGUOUS`.
9. If the tmux fingerprint changed and a valid controller snapshot is selected for logical restore, allocate new tmux objects with the saved refs and increment every restored generation/version. Otherwise treat live panes as unknown; no display-based auto-adoption.
10. Refresh observer sources; previously stored execution/attention state remains stale until confirmed.
11. Atomically write `layout-current.json`, publish a new epoch snapshot/event baseline, then become ready if no global fence remains.

### Layout snapshot

The snapshot contains schema/version, created time, server fingerprint, ordered workspaces, panes (`paneRef`, generation, workspaceRef, provider/session, cwd, display metadata) and an overall digest. It excludes tmux indices as identity, captures, instructions, hook bodies, operation history and secrets. Current snapshot is written by temp file + fsync + rename + directory fsync. History keeps 20 snapshots, spaced at least 30 minutes; an explicit maintenance snapshot may add one restore point.

## Crash-point outcomes

| Crash point | Restart behavior |
|---|---|
| before operation admission commit | no operation/effect; caller may retry |
| after admission, before prepared intent | operation was queued/accepted; mark failed on epoch loss unless safely requeueable before deadline |
| after prepared intent, before tmux | probe shows absent; mark failed-before-effect, do not silently execute |
| after tmux effect, before confirmation | probe confirms exactly once and commits recovered success |
| after effect, ambiguous evidence | mark `recovery-required`, fence overlapping refs, exact owner decision required |
| after DB success, before reply | same idempotency key returns stored completed result |
| while long provider operation runs | delivery/evidence cursors determine running/completed/unconfirmed; never resend automatically |
| while soft-remove timer waits | timer reloads from DB; version/lifecycle recheck prevents killing an undone/restored target |

## Hooks and spool recovery

The hook mode accepts a strict event kind plus hook JSON on stdin, caps input at 64 KiB, extracts only allowlisted fields (`provider`, session ID, transcript path digest, event kind/time, `$TMUX_PANE` locator), then opens the normal `control.sock`, authenticates as `hook-producer`, and calls only `hook.publish`. If unavailable, it creates a unique `0600` temp file under `hooks/`, fsyncs, atomically renames to `.ready`, and exits quickly. The controller processes files in name/time order through the same envelope validator with event-id dedupe, validates that the event belongs to a matching Cockpit pane/session, then deletes it. Maximum spool count/age are bounded; overflow becomes visible source degradation, not silent state assertion.

## External mutation detection

The reconciler maintains a digest of controlled topology/stamps and consumes tmux notifications/poll fallback. A change with no matching running/prepared operation is `external_mutation`:

- read projection updates to show reality and source degradation;
- implicated generations/versions increment;
- mutation on those refs fails until a focused reconcile confirms identity;
- a global fingerprint replacement fences all mutation;
- the audit records only actor=`external/unknown`, refs, before/after digests and time.

This detects but cannot prevent a malicious same-UID user from issuing raw tmux commands. Stronger hostile isolation would require a separate OS user/socket boundary and is outside the single-user scope.

## Break-glass lifecycle

Normal prepared path:

1. `break_glass.prepare` requires a local controlling TTY, explicit typed confirmation and the global barrier.
2. Controller rejects new writes, drains or terminally records queued operations, writes/fsyncs a snapshot, writes `break-glass.json` with `active=true`, epoch, nonce digest, operator and start time, closes the mutating socket and exits, releasing the lock.
3. `cockpit break-glass enter` acquires the same lock nonblocking and verifies the fence. It records `entered` and offers documented, fixed recovery actions plus attach/inspect. If a raw tmux shell is exposed, the banner states it is outside normal protocol and every resulting drift will force reconcile.
4. On clean exit it records a bounded after-topology digest and `active=false`, then releases the lock.
5. The controller restarts, sees the completed break-glass record, performs full reconciliation, bumps affected generations/versions, and only then becomes ready.

Emergency path when controller is dead: the helper must first fail a health connection, acquire the same lock, and atomically create an active fence before any tmux command. If the helper crashes, the kernel releases the lock but `active=true` remains; a restarted controller stays fenced. The operator runs `break-glass finish --recover` while holding the lock to close the record. Thus break-glass and normal mutation cannot both be active under the supported path.

## Retention and backup

- terminal operations/idempotency rows: 30 days; the `ik_<unix-seconds>_<random>` timestamp independently enforces the same replay horizon, so a pruned old key returns `IDEMPOTENCY_EXPIRED` rather than admitting a new effect; security/break-glass and recovery-required: 90 days;
- compact audit: rotate at 10 MiB, keep five files or 90 days;
- tombstone details: 90 days, then identifier digests for one year;
- binding history: last 10 per pane or 90 days;
- layout history: latest 20 spaced points;
- WAL checkpoints happen after clean snapshots/shutdown and size thresholds;
- backup copies a SQLite online backup plus current layout snapshot, never live `-wal` by file copy.

No durable log is written for state reads, capture reads, event delivery or waits. Mutation audit stores digests/byte counts, not instructions or captures.
