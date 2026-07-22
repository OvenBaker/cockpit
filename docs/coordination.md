# Structured Workstream Coordination (Coordination Core v0)

The coordination domain (`internal/coord`) replaces pane scraping for one
bounded cycle: **task publish → builder claim/lease → builder handoff →
exact-SHA review → acceptance → release handoff**. Semantic state lives only
in immutable typed records and controller events. Terminal text, pane titles,
captures, copy mode, and scrolling are never semantic inputs; the package has
no tmux access at all (enforced by `TestCoordinationHasNoTerminalAuthority`).

It extends the existing `cockpit-core` resident controller: one daemon, one
authenticated method registry, one SQLite store (schema version 2), one
CLI/MCP translation boundary. MCP and ctl reach identical controller methods
and error codes; neither owns policy or state.

## Records

Every record is strict JSON (unknown fields rejected), bounded
(≤128 KiB, lists ≤64 items), and stored immutably in a controller-private
content-addressed area (`<root>/coord/artifacts/<aa>/<sha256>`). SHA-256 is
computed over the exact stored canonical bytes (the transmitted `record`
value, surrounding whitespace trimmed). Publication stages + fsyncs the body,
hard-links it with no-replace semantics, then commits metadata, state
transition, event, projection bump, and idempotency result in **one**
transaction. Startup reconciliation verifies every committed body digest and
prunes never-committed orphans.

Caller-authored record types (validated schemas, `coord.<type>.v0`):

| recordType            | authored by  | drives transition                |
|-----------------------|--------------|----------------------------------|
| `workstream-contract` | operator     | workstream creation, role binding |
| `task-assignment`     | orchestrator | task publish / single correction |
| `builder-handoff`     | builder      | claimed → handoff-submitted      |
| `review-request`      | orchestrator | handoff-submitted → review-requested |
| `review-result`       | reviewer     | review-requested → reviewed-pass / reviewed-changes-requested |
| `final-acceptance`    | orchestrator | reviewed-pass → accepted         |
| `release-handoff`     | orchestrator | accepted → released              |
| `lease-transfer`      | orchestrator | scoped reviewer small-fix lease  |
| `plan-reference`      | orchestrator | none (supporting artifact)       |

Controller-authored records: `task-claim`, `write-lease`,
`task-acknowledgement`, `lease-return`, `workspace-checkpoint`. Bounded read
shapes: `coord.status.v0` (compact projection), `coord.event.v0`.

The live coord workstream's own `BUILD-001` envelope is the golden fixture
(`internal/coord/testdata/task-assignment-live.json`).

## Policy binding (policy-v1 amendment)

An approved **build-a-brief package** is the builder's complete primary input:
a requirements floor *and* scope ceiling. Coordination transports it without
starting another discovery cycle, and later findings can never silently amend
it. Two mechanisms make that durable:

**Repository policy assets.** The five frozen policy-v1 Markdown inputs
(operating contract + four role prompts) are copied byte-for-byte into
`docs/policy-v1/` with their source paths and SHA-256 digests recorded in
`docs/policy-v1/MANIFEST.json` (which also pins the brief-package artifact
hash). `TestPolicyAssetsMatchManifest` fails on any drift.

**Record binding.** Lifecycle records accept an optional bounded
`policy {version, briefPackageSha256}` object. The binding originates in the
task assignment; the invariant enforced on every transition is:

- a policy-bound task refuses any downstream handoff / review request /
  review result / acceptance / release record that omits the binding or
  carries a different version or brief-package hash;
- an unbound task refuses downstream records that try to introduce one;
- a correction revision must preserve the predecessor's exact binding.

A policy-bound review result must additionally **classify every finding** with
one of the policy triage classes, and classification constrains severity so
triage cannot be blocked-by-nitpick:

| class                | allowed severities |
|----------------------|--------------------|
| `in-scope-blocker`   | `blocker`          |
| `in-scope-material`  | `major`            |
| `valid-follow-up`    | `minor`, `note`    |
| `irrelevant-nitpick` | `minor`, `note`    |
| `reviewer-error`     | `minor`, `note`    |

Materiality follows `docs/policy-v1/operating-contract.md`: a finding may
block only for failed explicit acceptance, supported-flow regression,
realistic data/security harm, or inability to build/start/migrate/perform the
changed flow. The compact status projection exposes `policyVersion` and
`briefPackageSha256` per task for release-conductor consumption.

The current owner-authorized delivery limit remains **one bounded reviewer
and at most one correction loop** (enforced: revision ≤ 1); the durable
general policy's two-failed-review circuit breaker is a follow-up proposal
(see below), not authorization for a second correction.

## Roles and authority

Roles (`orchestrator`, `builder`, `reviewer`, `release-conductor`) are bound
to **credential-pinned client ids** at workstream creation. A mutation payload
can never claim or escalate a role; the service resolves the caller's role
server-side, and mutations from identities not pinned by a credential grant
are refused outright. Enforced invariants:

- Exactly one active write lease per workstream (partial unique index).
- Only the assigned builder can claim a task and acquire the lease; the
  orchestrator and reviewer are refused.
- The lease releases automatically on a valid builder handoff.
- The reviewer is read-only unless an orchestrator-published `lease-transfer`
  record grants a scoped (paths inside the task worktree, ≤24 h) lease; a
  verdict is refused while any lease is active, so the transfer must be
  returned (`lease_return`) first.
- Handoff/review/acceptance/release bind exact 40-hex head/base SHAs, the
  pinned plan hash, and prior record hashes; any mismatch fails with no
  mutation.
- Exactly one correction loop: revision 1 must reference revision 0's
  `review-result` findings; revision 2 is unrepresentable.

## Operations

All via `cockpit-core ctl --socket S --credential-file F [--client-id ID
--profile P] METHOD JSON` or the MCP tools in parentheses. Mutations carry
`workstreamId`, `expectedRevision` (projection CAS), and `idempotencyKey`
(`ik_<unix>_<32hex>`; same key + same intent replays the original result,
same key + different intent fails with `IDEMPOTENCY_CONFLICT`).

| method                            | capability  | role         | MCP tool |
|-----------------------------------|-------------|--------------|----------|
| `coordination.workstream_create`  | coord:admin | operator     | — |
| `coordination.task_publish`       | coord:write | orchestrator | `coord_task_publish` |
| `coordination.task_deliver`       | coord:write | orchestrator | — |
| `coordination.task_acknowledge`   | coord:write | builder      | `coord_task_acknowledge` |
| `coordination.task_claim`         | coord:write | builder      | `coord_task_claim` |
| `coordination.artifact_publish`   | coord:write | per record   | `coord_artifact_publish` |
| `coordination.artifact_read`      | coord:read  | any          | `coord_artifact_read` |
| `coordination.handoff_submit`     | coord:write | builder      | `coord_handoff_submit` |
| `coordination.review_request`     | coord:write | orchestrator | `coord_review_request` |
| `coordination.review_submit`      | coord:write | reviewer     | `coord_review_submit` |
| `coordination.lease_transfer`     | coord:write | orchestrator | — |
| `coordination.lease_return`       | coord:write | reviewer     | — |
| `coordination.acceptance_submit`  | coord:write | orchestrator | `coord_acceptance_submit` |
| `coordination.release_submit`     | coord:write | orchestrator | `coord_release_submit` |
| `coordination.checkpoint_emit`    | coord:write | orchestrator | — |
| `coordination.status_get`         | coord:read  | any          | `coord_status` |
| `coordination.events_list`        | coord:read  | any          | `coord_events` |
| `coordination.wait`               | coord:read  | any          | `coord_wait` |

`events_list` is cursor-based (`afterSeq`, limit ≤200) over a durable
append-only per-workstream log — no gaps, no pruning. `wait` is a one-shot
bounded wait (deadline ≤10 min) for any event past a cursor; waiters are
always removed on wake, deadline, or cancellation, and count against the
connection's wait limit. Release-conductor monitoring needs only
`status_get` + `events_list` + `wait`.

Error codes reuse the existing protocol vocabulary: `INVALID_REQUEST`,
`PERMISSION_DENIED`, `TARGET_NOT_FOUND`, `CONFLICT_VERSION` (stale
`expectedRevision`), `CONFLICT_MATERIAL_STATE` (wrong status / hash / SHA /
lease / delivery material), `IDEMPOTENCY_CONFLICT`, `DEADLINE_EXCEEDED`,
`CANCELLED`, `CAPABILITY_ABSENT`, `CONTROLLER_NOT_READY`.

## Provider-native task pointer delivery

`task_deliver` writes a private prompt file (`<root>/coord/seeds/<requestId>.prompt`,
0600, no-replace) containing only the typed pointer envelope:

```
coord.task-pointer.v0 workstreamId=… taskId=… revision=… requestId=… artifactPath=… artifactHash=…
```

and invokes the **pinned external seeded first-turn producer**
(`fix/cockpit-seeded-spawn`, reviewed exact head
`86c544ea24bf39f5b0718a5006316f2f6ad3c316`) through the material-binding
interface `--request-id --initial-prompt-file --initial-prompt-sha256
--initial-prompt-bytes`, plus `--cwd`/`--name` launch context and the typed
selective interaction profile. Coordination-controlled sessions are
agent-to-agent by definition, so the adapter always requests
`--interaction-profile agent` as a fixed value — no request can select the
human profile, and human/default launches elsewhere (e.g. Develop Brief)
are untouched. The producer binds the profile into its durable reservation
identity: replaying a request id with a different profile conflicts before
any launch. The profile changes communication behavior only; the
content-addressed build-a-brief package remains the complete primary input
and task authority. The launcher path comes from `COCKPIT_SEED_LAUNCHER`
(absolute); unset means delivery fails closed (`CAPABILITY_ABSENT`). There
is no send-keys or shell-injection path, and the coordination package cannot
express one.

Delivery is two-phase and durable: reservation (status `prepared`) commits
before any launch side effect; the launch outcome (`launched`/`failed` with
the producer's terminal exit codes 2/4/5) commits after. Replaying the same
request id with identical material reconciles to the one canonical pane;
changed material conflicts before any side effect. The prompt file is
re-verified (regular, non-symlink, private, exact digest/length) immediately
before every launch. Acknowledgement (`task_acknowledge`) is a structured
builder record bound by request id + task identity + exact artifact hash —
wrong hash is refused without mutation.

## Durability

SQLite WAL + `synchronous=FULL` + foreign keys, one connection. Every
mutation is a single transaction; crash/restart preserves artifacts, tasks,
claims, leases, deliveries, the event cursor, the projection, and idempotency
results (covered by in-process restart tests and a hard process-kill in the
rehearsal). Artifact bodies are content-addressed, so replayed publication is
naturally idempotent; corruption of a committed body fails controller startup
(`CONTROLLER_NOT_READY`).

## Choir-flow rehearsal and cutover runbook

- `TestCoordRehearsalFullCycle` is the deterministic isolated rehearsal: a
  real daemon process, throwaway tmux server, isolated git repo/worktree, and
  fake seeded launcher drive the complete cycle, including a mid-cycle crash
  and terminal-noise injection.
- `coordination.checkpoint_emit` is the characterization wrapper for a
  current Cockpit workspace: it emits an immutable
  `workspace-checkpoint` record built **only** from the durable controller
  projection (workspace/pane identity rows — refs, generations, versions,
  badges, fences). It never reads pane output and cannot interrupt a live
  turn.
- Live cutover procedure (release-conductor sequenced, out of scope here):
  1. Wait for an externally verified clean handoff on the target workstream.
  2. `checkpoint_emit` the workspace as provenance.
  3. Create the workstream/contract, publish the task envelope pinned to a
     refreshed base SHA, and deliver via the seeded capability once it is in
     the deployed `main`.
  4. Never migrate a workspace mid-turn; absent a verified clean checkpoint,
     stay on the deterministic rehearsal.

## Follow-up proposal: review circuit breaker (not implemented)

The policy-v1 operating contract's general rule — at most **two** failed
review rounds, then a mandatory stop and orchestrator escalation triage — is
materially expansive relative to the shipped one-correction state model (new
terminal state, per-workstream configuration, a new record type, and a
resumption path), so per the amendment it is checked in here as a concrete
follow-up rather than implemented. Proposed shape:

- **Schema.** `WorkstreamContract.reviewLimits {maxFailedReviewRounds: 1..2}`
  (default 1, preserving today's behavior); new record type
  `coord.escalation-triage.v0` `{workstreamId, taskId, taskRevision,
  createdAt, createdByRole: orchestrator, headShaOrCheckpoint,
  unresolvedFindings: [{findingId, reviewResultSha256, class, evidence}],
  briefAmbiguityAssessment, recommendedDisposition: one-bounded-repair |
  amend-brief | accept-with-follow-ups | split-work | owner-judgment,
  policy}`.
- **Storage.** `coord_tasks.failed_review_rounds INTEGER NOT NULL DEFAULT 0`
  (schema v4, forward-only); increment inside the `review_submit` transaction
  when the verdict is `CHANGES_REQUESTED`.
- **Transitions.** When `failed_review_rounds == maxFailedReviewRounds`, the
  task moves to a new terminal-until-triage status `halted-circuit-breaker`
  instead of `reviewed-changes-requested`; in that status `task_publish`
  (correction), `handoff_submit`, and `review_request` are refused with
  `CONFLICT_MATERIAL_STATE`. A new orchestrator-only operation
  `coordination.escalation_submit` publishes the triage record (event
  `escalation.published`) and its `recommendedDisposition` gates exactly one
  resumption path (`one-bounded-repair` re-opens a single correction;
  everything else ends the task revision line).
- **Tests.** Second `CHANGES_REQUESTED` verdict halts the task with no
  further mutation possible; an unclassified triage record is refused; a
  `one-bounded-repair` disposition permits exactly one more correction and a
  third failure is unrepresentable; crash/restart preserves
  `failed_review_rounds` and the halted status; status/events expose the
  halt for release-conductor monitoring.

## Test map

- Schemas / golden corpus: `internal/coord/records_test.go` (live `BUILD-001`
  fixture + invalid corpus).
- Policy binding, finding-class triage, and policy-asset integrity:
  `internal/coord/policy_test.go`.
- Fail-closed matrix (roles, stale revisions, hash mismatches, duplicate
  lease, idempotency conflicts — all no-mutation): `service_test.go`.
- Delivery, hash-bound acknowledgement, prompt drift/symlink, launcher exit
  codes: `seed_test.go`.
- Static authority boundary: `authority_test.go`.
- End-to-end rehearsal, crash/restart, CLI/MCP parity, terminal-noise
  immunity, no-send-keys proof: `internal/core/coord_rehearsal_test.go`.
