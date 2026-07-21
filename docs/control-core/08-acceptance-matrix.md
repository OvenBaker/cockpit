# False-pass-resistant acceptance matrix

## Test architecture

The controller is tested at four layers:

1. **Pure:** identity/state/attention/capability/CAS/error/idempotency functions with deterministic clocks and property tests.
2. **Store/protocol:** real SQLite, framed socket, schema corpus/fuzzing, cancellation and migration tests.
3. **Driver integration:** real tmux 3.5a on a unique throwaway socket, fixed fake Claude/Codex processes and deterministic hook/transcript fixtures.
4. **Cross-process system:** daemon plus separate CLI/MCP/web/Orbital test processes racing, with fault-injection process kills and a recorded tmux argv/effect trace.

### Live-system safety guard

Every integration test creates a `mktemp` root and random socket name `cp-it-<test>-<random>`, sets isolated runtime/state/provider roots, and starts its own tmux server. The test binary refuses:

- socket name exactly `cockpit`;
- a runtime/state path outside its temp root;
- inherited `TMUX`, production client tokens or production state paths;
- any server fingerprint not minted by the test.

The harness checks before and after that `tmux -L cockpit` was never present in the driver trace. Tests kill only the exact random test server after validating its fingerprint.

### Strong oracles

For every “wrong target untouched” test, non-target panes contain:

- a unique long-running fake process with recorded PID/start time;
- a unique sentinel line in scrollback;
- unique `@test_sentinel` and identity stamps;
- captured topology/options hash before the race.

Passing requires the expected structured error/result **and** unchanged non-target PID/start time, scrollback sentinel, options hash and absence from mutating driver argv. A generic nonzero exit is not a pass.

Fault injection points are named barriers (`after_admission`, `after_prepared_commit`, `after_tmux_effect`, `after_result_commit`) acknowledged over a test pipe. The harness kills the daemon only after the named barrier, eliminating timing-luck tests.

## Required race and failure sentinels

| ID | Setup and action | Required oracle | False-pass resistance |
|---|---|---|---|
| R1 pane-index sibling deletion | Build A `.0` and intended B `.1`; record B tuple/sentinels; remove A so B becomes `.0` and locator observation increments B version; submit B mutation with the old B tuple | exactly `CONFLICT_VERSION`, zero effect; B and every other pane unchanged | assert committed locator/version increment occurred before submit; mutation driver trace is empty; B/non-target PID/options/scrollback hashes unchanged |
| R1b poisoned client locator | Corrupt the client’s cached `PaneView.locator.displayTarget` to name another pane, then have the real client builder create a mutation from B’s fresh stable tuple | serialized request contains no locator/display target; with no concurrent drift exactly B changes; cached-locator pane untouched | schema-decode the recorded frame and reject any locator field; trace names only B’s current `%pane_id`; all non-target sentinels unchanged |
| R2 delete/recreate same position | record A tuple; hard-delete/tombstone A; create C at same display position; issue old A mutation | `TARGET_GONE` or `CONFLICT_GENERATION`; C untouched | assert C has new paneRef and no C tmux ID in mutation trace |
| R3 two clients same version | two OS client processes pause/nudge or metadata-write same pane with identical expected version and different idempotency keys; release barrier simultaneously | exactly one effect; other `CONFLICT_VERSION` (V1 has no commutative mutations) | count fake-provider inputs/tmux effect trace equals one; final version increments once |
| R4 idempotency same intent | replay same key/intent before and after daemon restart | same operationRef/result, one effect | trace/effect counter equals one; response’s `replayed` evidence checked |
| R5 idempotency different intent | same caller/method/key, change payload or target/expectation | `IDEMPOTENCY_CONFLICT`, no second effect | HMAC record unchanged; target sentinels unchanged |
| R5b expired idempotency key | use a syntactically valid key timestamped >30 days ago, once with a retained fixture row and once after pruning it | `IDEMPOTENCY_EXPIRED` both times; zero admission/effect | operation/effect counts remain zero and outcomes match regardless of row presence; a >5-minute-future key is also rejected |
| R6 crash after effect | kill at named `after_tmux_effect` before result; restart; resend same key | operation-specific probe commits exactly-once recovered result; no duplicate | fake spawn/input counter and tmux topology prove one; restart must not invoke effect builder again |
| R7 stale hook/transcript disagreement | fresh transcript says working; stale hook says idle, then inverse with invalid session ID | selected state follows precedence/freshness; disagreement/source health visible | response must include both source health codes; cannot pass by returning `unknown`/dropping stale source silently |
| R8 MCP wait cancellation | MCP process starts wait, server confirms watcher registered, then JSON-RPC cancel/stdio close | cancelled result or clean disconnect; watcher count returns baseline; no later mutation/event delivered to closed waiter | inspect debug test metric and heap/goroutine bound; trigger later matching event and assert no output/effect |
| R9 oversized/control capture | fake pane emits >limit ANSI/OSC/C0/C1, invalid UTF-8 and secret-pattern text | valid UTF-8 <= max, controls removed, redaction/truncated flags correct | byte-scan response for forbidden sequences; canary secret absent; non-target unchanged |
| R10 oversized/control instruction | send >16,384 bytes, NUL, ESC/OSC and invalid UTF-8 variants to nudge/resume | `INVALID_REQUEST`; zero buffer/input/effect | fake provider stdin/turn count remains zero; tmux buffer list unchanged |
| R11 unauthorized Web/Orbital | forge email header without JWT; valid web identity asks capture/break-glass; Orbital asks capture/foreign recover | gateway 403 for forged assertion; controller `PERMISSION_DENIED/CAPABILITY_ABSENT`; zero effects | use real gateway middleware with test keys/AUD; ensure denied request never reaches driver queue |
| R12 break-glass exclusion | daemon holds lock then helper enter; separately helper holds lock/fence then daemon start | first helper fails before tmux; second daemon stays fenced/read-only; never two effect traces | OS-process lock holders and barrier prove overlap attempt; check global mutation counter max concurrency = 1 |
| R13 legacy/new convergence | legacy shim and new client race same target/version/key semantics | both calls arrive as controller operations; same CAS/idempotency rules; one writer | PATH uses instrumented fake tmux that fails if legacy script invokes directly; driver is sole recorded writer |

## Additional identity, lease and recovery matrix

| ID | Scenario | Acceptance oracle |
|---|---|---|
| I1 | move pane across workspaces and reindex repeatedly | paneRef/generation stable; version increments per material move; current locator changes |
| I2 | recover respawns same intended conversation | paneRef stable; generation/version increment; process fingerprint changes; session binding confirmed |
| I3 | retarget to new conversation | paneRef stable; generation/version increment; old/new binding history bounded; stale old expected request conflicts |
| I4 | hook reports provider `/clear` new session | paneRef stable; generation/version increment once; teardown event from old session cannot overwrite it |
| I5 | soft remove then undo | same ref/generation, versions advance, timer cannot later kill restored pane |
| I6 | hard remove then new pane | old tombstone retained; new ref even if tmux reuses display index or later numeric ID |
| I7 | daemon restart with surviving tmux | epoch changes; refs/generation/version unchanged when evidence matches; stream demands resync |
| I8 | full tmux rebuild from selected snapshot | workspace/pane refs restored, every restored generation/version increments, all tmux IDs/fingerprint replace old |
| I9 | full tmux replacement without selected snapshot | all live objects quarantined; no old ref attached by name/index/session guess |
| L1 | two daemons launch simultaneously against no socket | exactly one acquires lock and can become ready; loser issues zero tmux/store writes |
| L2 | stale socket and bogus live/stale PID contents | lock owner safely replaces only verified socket; PID content has no authority |
| L3 | non-socket/symlink at socket path | daemon fails closed without unlinking target |
| L4 | abandoned break-glass active record after helper SIGKILL | new daemon acquires released lock but remains fenced; no normal mutation until explicit finish/reconcile |
| C1 | multi-resource opposite-direction moves arrive concurrently | sorted lock rule prevents deadlock; one succeeds and the other conflicts or releases every claim and fails `QUEUE_CONFLICT`; no in-place lock-set recomputation or partial topology |
| C2 | topology changes between admission and queue head | execution-time CAS catches drift; zero effect |
| C3 | deadline expires while queued | terminal `DEADLINE_EXCEEDED`, effect flag false, no later action |
| C4 | client disconnects after accepted long op | operation continues/records according to method; later `operation.get` is authoritative; no implicit cancellation/resend |
| X1 | direct external tmux writer alters controlled option/topology | external mutation event, affected generation/version bump and fence; no silent repair/wrong binding |
| X2 | duplicate/conflicting paneRef stamps | both objects quarantined/fenced; never pick first by order |

## Operation semantics matrix

| ID | Operation | Required proof |
|---|---|---|
| O1 | spawn | exactly one stamped pane/process; returns paneRef before optional later session adoption; crash points absent/applied/ambiguous classified |
| O2 | resume existing | duplicate live session rejected across all workspaces/providers; no second process/transcript writer |
| O3 | nudge | only stable waiting pane accepts; literal text arrives once; new turn evidence completes; title prefix alone is insufficient |
| O4 | pause | fixed interrupt is the only interaction allowed while working; explicit aborted/waiting evidence completes; timeout never repeats Escape |
| O5 | compact | fixed `/compact`; running observable after client exit; only explicit `Compacted` + waiting completes; elapsed timeout alone fails unconfirmed |
| O6 | resume instruction | requires paused/stopped material state and bounded acknowledgement; new provider activity completes |
| O6b | continue stopped process | local-only exact pane tuple plus exact PID/start-ticks/process-group fingerprint; one fixed continue; liveness/provider evidence completes |
| O7 | explicit replay | prior op and content digest/current CAS required; changed content conflicts; replay creates linked operation and at most one new deliberate effect |
| O8 | recover/reboot | exact paneRef/session; working state rejected unless local privileged override explicitly present; process changes once |
| O9 | move | same stable pane under exact destination; both workspace versions change atomically in projection |
| O10 | soft remove/undo/expiry | durable timer survives daemon restart; undo/version race cannot kill restored/replaced target |
| O11 | display metadata | cannot set state fields; control/length validation; two-client CAS; event cursor advances once |
| O12 | snapshot/reconcile | global barrier excludes mutations; snapshot digest verifies; unknown/conflicting objects reported rather than guessed |

## Protocol, capability and privacy matrix

| ID | Scenario | Required proof |
|---|---|---|
| P1 | frame length >1 MiB, truncated frame, invalid UTF-8/JSON, unknown mutation field | connection/request fails boundedly; daemon remains healthy; no admission/effect |
| P2 | protocol major mismatch | `UNSUPPORTED_PROTOCOL` with supported range; no partial session |
| P3 | client claims another profile/client ID | connection profile comes from credential mapping, claim ignored/rejected |
| P4 | operation capability changes after admission | execution-time capability/state recheck fails before effect |
| P5 | event ring gap or daemon epoch change | `resync-required` + fresh snapshot path; no missing change represented as timeout |
| P6 | change occurs between wait evaluation and registration | waiter matches exactly once due snapshot/register critical section |
| P7 | 257th global watcher / 65th per connection | bounded rejection, existing watchers unaffected |
| P8 | MCP manifest/annotations | no exec/tmux/key tools; wait/capture marked read-only, interactions/lifecycle/metadata marked writes |
| P9 | raw instruction/capture/audit search | canary secret appears only in ephemeral fake-provider path/authorized response, never DB/audit/log/error/snapshot |
| P10 | error response to restricted client | no stack, raw argv, foreign target IDs, private paths or other caller ID |
| P11 | web JWT variants | bad signature/issuer/AUD/exp/nbf/alg/unknown kid fail; valid token + allowed identity passes; JWKS outage uses only valid bounded cache then fails closed |
| P12 | CSRF/CORS/Origin | cross-origin POST/stream rejected before controller; same-origin valid session obeys capabilities |
| P13 | Orbital ownership | it can recover only the paneRef correlated to its execution; display label collision cannot grant control |
| P14 | hook outside Cockpit / wrong session / replay / spool overflow | no state mutation for invalid events; source health reports invalid/degraded; spool remains bounded |
| P15 | daemon dies after creating a controller input buffer | restart deletes only orphan buffers with valid controller namespace and terminal/absent opRef; foreign tmux buffers survive; input is not resent |

## Migration and rollback matrix

| ID | Scenario | Required proof |
|---|---|---|
| M1 | read-only shadow | syscall/driver trace has zero tmux writes and zero production state writes |
| M2 | controller state authority rollback | old poller/state can resume reading inert new stamps; identities DB retained |
| M3 | each family cutover | owned legacy script refuses direct write; feature switch selects exactly one path |
| M3b | state-authority legacy transition ticket | shim reserves exact resources, receives/stamps nonce and completes through controller; concurrent normal operation is blocked; wrong/missing nonce, timeout and membership drift fence refs | driver trace shows no un-ticketed legacy effect; max overlapping writer count is one; all claims released after terminal outcome |
| M4 | family rollback | controller drained/snapshotted before old writer enabled; max concurrent writer trace remains one |
| M5 | old Orbital provider migration | map only exact unique pane-id + derived-name match with confirmation; collision/missing remains unresolved |
| M6 | crash after Cockpit spawn before Orbital launch record | same idempotency key returns one paneRef; Orbital attaches without second spawn |
| M7 | new and legacy Orbital records coexist | new uses paneRef; legacy translation is read/recover-only and never guesses by label |
| M8 | repository static boundary | production tmux mutation verbs occur only in private driver and explicit break-glass; tests/attach exception allowlisted by path |

## Exit criteria

A slice/phase passes only when:

- all applicable rows run on a real throwaway tmux server in CI/local acceptance;
- race rows use separate OS processes and synchronized barriers, not sequential calls or goroutines alone;
- fault tests kill at acknowledged named barriers;
- target and non-target side effects are asserted, not inferred from response status;
- watcher/goroutine/file-descriptor counts return to a bounded baseline;
- rerunning the test proves isolation and idempotency;
- production Cockpit socket absence from traces is asserted;
- failures retain artifacts (driver trace, DB copy, topology snapshots) inside the test temp root for diagnosis without private real-session data.
