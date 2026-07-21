# Cockpit control-core Slice 0/1 evidence

Date: 2026-07-21.  Worktree: `feat/cockpit-control-core-v1` at clean-main
`20f75ea`, with only the changes listed by `git status` below.  All runtime
tests create a temporary root and a random `cp-it-*` tmux socket.  The daemon
rejects the literal socket name `cockpit`, and every integration fixture reads
its recorded `driver.trace` on teardown and fails if `-L cockpit` appears.

Remediation worktree: `/home/gareth/workspaces/lean/cp-core/orderlies-implementation`
on `feat/orderlies-controller-domain`; it is separately uncommitted. The real
Orderlies fleet now sources `runtime/controller-domain.sh`, which validates
scalar `ORDERLIES_TMUX_SOCKET`/`ORDERLIES_SESSION` values and defaults both to
`orderlies`. `tests/orderlies-isolation.sh` invokes the real launcher’s
no-effect `--check-controller-domain` mode and statically rejects a Cockpit
socket hard-code in the six controller-domain scripts. It also runs the real
`--bootstrap-only` first-start path twice on a random socket, asserting exactly
one stamped `aide` workspace and no temporary bootstrap window; this mode never
launches an agent, poller or network action.

| Gate | Command | Observable artifact/oracle |
|---|---|---|
| Canonical schema, golden corpus, error/capability registries | `CGO_ENABLED=0 GOPROXY=off go test ./... -count=1`; `bash tests/check-protocol.sh`; `npx tsc --project tsconfig.json` | `TestProtocolSchemaGoldenCorpus` resolves and executes the strict JSON Schema against all 11 Slice 0/1 method fixtures plus unknown-envelope/query/mutation, malformed-expectation and unsupported-protocol cases. The runtime transport test asserts the relevant `INVALID_REQUEST`/`UNSUPPORTED_PROTOCOL` outcomes. |
| Pure-Go SQLite driver | `CGO_ENABLED=0 GOPROXY=off go test ./... -count=1` | `go.mod` pins `modernc.org/sqlite v1.53.0`; store uses `database/sql` driver name `sqlite`; the complete suite builds and passes with CGO disabled. |
| Current Bash snapshot characterization | `bash tests/bash-characterization.sh` | A random tmux server yields the existing `@active` snapshot, pane metadata and current `@window_id`; no live socket is used. |
| Orderlies isolation | `ORDERLIES_SOURCE_ROOT=/home/gareth/workspaces/lean/cp-core/orderlies-implementation bash tests/orderlies-isolation.sh` | The actual sibling launcher sources its validated scalar contract, rejects unsafe names and any fleet `-L cockpit` hard-code, and bootstraps an `aide` workspace twice on a random socket with a temporary HOME cache only. |
| R3, O11 | `CGO_ENABLED=0 GOPROXY=off GOCACHE=/tmp/cp-core-go-cache go test ./... -count=3` | `TestSlice01Acceptance/R3_O11` starts two separate `cockpit-core ctl` processes with one tuple: exactly one wins and the loser is `CONFLICT_VERSION`; the trace delta and cursor delta are each exactly one. It rejects state/control/ASCII-overlength and 49-rune badges with zero cursor/effect change, and verifies a second pane’s sentinel is untouched. |
| C2, C3 | same | `.../C2_C3_queue-head` holds the first badge operation at the named `badge_at_queue_head` barrier. A second same-version request reaches execution after the first effect and returns `CONFLICT_VERSION`; a future deadline expires while queued and returns `DEADLINE_EXCEEDED`, with no `queue-expired` driver effect. |
| R4, R5, R5b, I7 | same | `.../R4_R5_R5b_I7` asserts the same exact operationRef on initial/replay/post-restart replies and one `idem` driver effect; changed intent conflicts; a retained old idempotency row and then its pruned/absent form both return `IDEMPOTENCY_EXPIRED` without admission; exact-stamp restart preserves generation/version. |
| R6 | same | `.../R6` makes the daemon exit at the named `after_tmux_effect` barrier; restart probes the stamped badge, and the trace has exactly one `recovered` builder effect. |
| L1--L3 | same | `.../L1_L2_L3` concurrently launches two no-socket daemons and records exactly one test store actor; its stale Unix listener plus bogus PID test uses a separate random tmux server and waits for replacement inode plus authenticated health; regular-file and symlink socket paths fail before DB/driver-trace creation and are never unlinked. A connectable socket fails closed. |
| P1--P7 and wait cleanup | same | `.../P1_P2_P3_P5_P6_P7` sends distinct oversized, truncated, invalid-UTF-8 and invalid-JSON frames and proves health/no admission; it verifies protocol-major/profile binding, capability recheck, epoch resync, replayable subscription notification, atomic wait registration/match, same-connection `rpc.cancel`, connection-close cleanup, operation-terminal waiting, 64 waits/connection and 256 global waits. Cancelled watchers return to baseline and cannot receive a later delivery. |
| Event ring/subscription gates | `go test ./internal/core -run 'TestEventRing|TestSubscriptionOwnership'` | Direct deterministic tests prove cursor replay ordering, stale-cursor resync, overflow resync (never silent drop), 256 subscription cap and idempotent cleanup. Subscriptions are attached/owned before their response and notifications start only after it is written. |
| Identity/stamp fence | `go test ./internal/core -run TestStableIdentityMultiPaneAndStampFence` | First inventory assigns two panes in one tmux window one workspaceRef; a changed durable pane-version stamp fences restart with `CONTROLLER_NOT_READY`. The controller also verifies its persisted server fingerprint. |
| SQLite V1 | `go test ./internal/core -run TestStoreV1PragmasVersionAndFutureRefusal` | One writer connection, WAL/foreign keys/busy timeout/FULL synchronous settings, transactional `user_version=1`, integrity check and future-version refusal are executable. |
| Formatting and diff integrity | `gofmt -w cmd/cockpit-core/main.go internal/core/*.go`; `git diff --check` | No formatter or whitespace error. |
| Static mutation boundary | `bash tests/tmux-mutation-boundary.sh` | Production Go tmux mutation verb literals are limited to `internal/core/tmux.go`; `_test.go` fixtures are excluded by path. |
| Fault-hook build separation | `CGO_ENABLED=0 GOPROXY=off go build -buildvcs=false -o /tmp/cockpit-core-normal ./cmd/cockpit-core`; `strings /tmp/cockpit-core-normal` | Normal binary contains no `COCKPIT_TEST_`, `hold-`, or `disable-metadata` test controls; tagged source is excluded from the production source-symbol gate. Integration builds its daemon with `-tags cockpit_test`. |

Observed final suite output:

```text
?    github.com/gareth/cockpit-core/cmd/cockpit-core [no test files]
ok   github.com/gareth/cockpit-core/internal/core 4.017s
```

## Slice 1 exit artifact

`TestSlice01Acceptance` is the executable Slice 1 system-test artifact. Its
subtests use distinct `cockpit-core ctl` processes for mutation races and raw
framed client connections for same-connection cancellation, atomic wait and
watcher limits. It retains the per-test `driver.trace`, SQLite DB and tmux
topology under the temporary test root until test teardown. It does not claim
MCP cancellation (MCP is excluded from Slice 1).

## Explicit Slice 1 exclusions

No provider observers, transcripts/hooks, capture, spawn/resume, input
injection, process control, MCP/web/Orbital, layout restore, multi-resource
mutations, service units, production socket access, migration/cutover, or
break-glass actions are implemented.  `metadata.set_display` is deliberately
badge-only; labels and observed/attention state are not writable.

## Architect-remediation gates

`TestFreshRootCannotAdoptStampedServer`, `TestCloseOnlyUnlinksOwnedSocketInode`,
`TestPreparedPartialEffectFencesPane`, and
`TestConflictingIdentityStampsFenceBeforeAdoption` are direct random-tmux
oracles for controller ownership, listener inode ownership, ambiguous prepared
effects, duplicate workspace/pane refs, and partial stamps. `R3_O11` binds its
target to `slice:0.0` by pane ID and separately checks the `slice:0.1` option
sentinel and mutation trace. The event acceptance test holds the named
`subscribe_after_snapshot_before_register` barrier while a real effect lands,
then proves the subscriber receives it. Runtime tests additionally exercise
standard JSON-RPC `-32700`, `-32601`, and `-32602` responses. Orderlies now
uses a socket/session-keyed poller pidfile and reports `home=orderlies`.

Final closure adds a server-scoped advisory lease beside tmux's canonical
socket path, acquired before fingerprint preflight and held for daemon
lifetime. `Close` serializes concurrent callers and only unlinks its recorded
listener inode. The Orderlies poller now holds a domain-keyed advisory flock;
candidate launches never kill or trust a stale PID, and check mode reports the
pidfile and lockfile without effects.

## Lead-audit closure oracles

The process-level lease gate is
`TestServerLeaseTwoFreshRootsProcess`, not the earlier in-process helper test.
It builds the tagged daemon, starts two fresh-root OS processes together on one
unstamped random tmux server, requires exactly one authenticated
`controller.health.ready=true` socket, and requires the loser to exit with no
`control.db` or `driver.trace`. After stopping only the winner it proves both a
third fresh root and a root with a precreated empty SQLite `control.db` fail on
the persisted fingerprint before a driver mutation.

`TestEventSubscriptionOwnershipAndConnectionCleanup` uses two authenticated
wire connections: foreign unsubscribe gets `PERMISSION_DENIED`, the owner still
gets the next event, owner release gets no later delivery, and connection close
returns `subscriptionCount` to baseline. `TestOperationFailureWaiterRegisteredBeforeTransition`
holds `after_prepared_commit`, registers an operation waiter before capability
removal, and proves exactly one `operation.failed` wake/event and cleanup.

`TestProtocolEnvelopeIDsAndOutstandingBounds` asserts `-32600` for unknown
envelope fields, invalid IDs, and duplicate in-flight wait IDs; it asserts
`-32602` for malformed cancel IDs, forbidden health parameters, and ordinary
required parameter shapes, while preserving the first 64 waits when the 65th
is rejected. `TestBadgeRequiredFieldsOneAtATimeNoAdmissionOrEffect` separately
empties each mutation required field and separately supplies zero/two
expectations, checking the exact standard/domain class plus zero operation,
event-cursor, and atomic-effect deltas for every case.
`TestSessionOpenStrictTaxonomy` covers canonical required session fields and
claimed-profile enum checks (`-32602`), nonempty unsupported protocol
(`UNSUPPORTED_PROTOCOL`), bad credentials (`UNAUTHENTICATED`), and pre-auth
parse/invalid-ID response taxonomy with `id:null` where required.
`TestPostEffectAmbiguityFencesAndWakesExactlyOnce` exercises deterministic
driver and readback ambiguity hooks, asserting durable `recovery-required`, a
fenced capability-free pane, one terminal wake/event, and no subsequent atomic
effect. `R6` additionally checks the successful crash-recovery audit adds one
row with the original `test-local-operator` caller.

`TestSlice01Acceptance/R3_O11` now records the non-target pane PID and
`/proc` start ticks, unique captured scrollback, and canonical topology/options
hash; it verifies all remain unchanged and that the non-target pane ID is not
in the atomic mutation argv. It verifies the race winner badge and old+1
durable/tmux versions, and separately proves valid O11 one-event/one-plan and
per-rejection zero trace/cursor deltas. The Orderlies flock gate waits for the
first candidate's `acquired` acknowledgement (not a timing sleep), cleans its
sentinel through an EXIT trap, and documentation names the dedicated Orderlies
tmux domain rather than Cockpit.
