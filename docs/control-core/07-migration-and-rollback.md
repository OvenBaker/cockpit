# Strangler migration and rollback plan

## Branch/worktree starting point

Implementation should start from clean Cockpit `main` at or after `20f75ea`, never from the current dirty `feat/lean-runner-gate-badge` worktree.

Recommended owner-executed order after this design is accepted:

1. Record this package and the already captured feature identifiers (`cacaae7`; uncommitted `cockpit`/`lib.sh`) in the implementation issue.
2. Because the owner declared the changes disposable, discard/delete that feature branch and its uncommitted work using the owner’s normal workflow. This design exercise does not perform it.
3. Create a fresh implementation branch/worktree such as `feat/cockpit-control-core-v1` from updated clean `main`. Do not reuse `/home/gareth/repos/cockpit` while it contains unrelated owner work.
4. Keep Orbital implementation in a separate branch/worktree from current clean `main` (`99c3a59` or later). Do not reuse the dirty `choir-g16` worktree or disturb its owner edits.
5. Land Cockpit protocol/client compatibility before changing Orbital’s provider identity.

Do not merge the current gate-badge commit. If Runner gate attention remains desirable, implement it later as a typed observer keyed by owning run/execution identity and explicit freshness; the cwd-keyed override is not a suitable identity contract. Do not carry forward the uncommitted TSV→JSON/history work: the controller SQLite/snapshot design supersedes it, although its atomic rename/history characterization tests are useful evidence.

## Migration invariants

- At every mutation-family cutover there is exactly one selected writer path; a compatibility script calls the controller or remains the sole legacy writer, never both.
- Shadow comparisons are read-only and use a throwaway or live read connection only; they never “repair” differences.
- Rollback changes routing/configuration, not durable identity history. New refs/tombstones are retained.
- Capability absence stays absence. A compatibility path cannot expose raw tmux/key/exec to compensate.
- Every phase has a sentinel proving which executable actually issued tmux mutation argv.
- `orderlies-up` is moved to a separate `tmux -L orderlies` server before Phase 3. This is the selected V1 disposition: orderlies remain useful but are outside Cockpit identity, projection and mutation authority. Phase 3 is blocked until the separate-socket trace is proved.

## Phase 0 — contract freeze and characterization

**Changes**

- Add clean-main characterization tests for every inventory row using only unique throwaway tmux sockets/runtime/state roots.
- Capture golden current projections and behavior for spawn, resume, retarget, reboot, move, soft remove/undo, workspace close/undo, idle parking, hooks/adoption, layout restore and web validation.
- Add a driver-trace test harness which records argv, target tmux IDs and before/after topology.
- Check in canonical V1 JSON Schema, generated Go/TS types, error/capability registry and source manifest.
- Characterize `orderlies-up`, move it to the dedicated `tmux -L orderlies` socket, and prove its argv never names the Cockpit socket. A future controller-client integration is optional and separately designed.

**Gate**

All current behavior intended to preserve has a throwaway-socket test; unsafe behavior is recorded as “must change,” not golden success. Tests prove they cannot connect to `tmux -L cockpit`.

**Rollback**

Test/schema-only; remove new files. No runtime/state effect.

## Phase 1 — Go core and read-only shadow controller

**Changes**

- Build the one Go binary, lease/store migrations, private tmux query driver, stable-ref projection, pure state classifier, source health, framed RPC, `cockpit ctl state`, operations/events/waits.
- Run `daemon --shadow --socket <throwaway/read-only>` beside the legacy poller on live Cockpit only after tests; it must not hold the production mutation lease, write tmux/options/layout or accept mutation.
- Compare canonical projection to `cockpit-state` and poller options over representative Claude/Codex/adoption transitions. Differences are classified as intended fix, source degradation or defect.

**Gate**

24 hours representative dogfood or a bounded equivalent trace replay; zero live writes from the shadow binary; identity remains stable across observed reindex/move; source disagreement is visible; event/wait leak tests pass.

**Rollback**

Stop the shadow process and delete only its isolated shadow DB/runtime directory. Legacy system is untouched.

## Phase 2 — first vertical mutation and controller lease

**Changes**

- Ship `metadata.set_display` for pane badge/label as the first end-to-end mutation, initially on throwaway socket, then behind an explicit production feature switch.
- Assign/stamp stable refs through a one-time reconcile while holding the controller lease. Legacy scripts remain disabled during the bounded switch window.
- Convert any badge setter chosen for the canary to `cockpitctl`; controller proves CAS, idempotency, per-pane queue, event/wait and crash-after-effect recovery.
- In production transition mode, a controller-owned `authority-mode` states which mutation families it owns. A legacy script for an owned family refuses direct execution.

**Gate**

All Slice-1 acceptance rows pass, including two-client same-version race and crash-after-effect. Driver trace proves one writer. Roll back once as a drill.

**Rollback**

Disable the metadata route, stop daemon after snapshot, leave stable stamps/DB in place, and restore the previous display setter. Stamps are inert to old Cockpit. Do not delete DB/refs.

## Phase 3 — state authority (poller strangled)

**Changes**

- Move hook ingestion, Claude/Codex transcript classification, adoption, attention latch, source health, chrome metadata and layout snapshot into controller.
- Change `cockpit-hook` to the bounded hook client/spool.
- Change `cockpit-state` to a thin `state.snapshot` compatibility client preserving old JSON fields plus optional new refs.
- Stop `cockpit-poller`; controller writes projected tmux options only for legacy chrome compatibility.
- Existing unowned mutation families may temporarily remain only through a private **transition-ticket** shim; direct legacy tmux writing without a ticket is forbidden once the controller owns state. Before effect, the shim calls a local-only transition endpoint with exact pane/workspace expectations and declares a bounded effect kind. The controller acquires the complete sorted resource set, blocks overlapping normal operations, allocates any new refs, commits a ticket/op nonce and returns a fixed effect plan. The shim stamps that nonce as part of its legacy effect, then calls completion; the controller probes exact postconditions and commits versions/result before releasing claims.
- Ticket timeout, missing/wrong nonce, topology membership drift, partial effect or ambiguous probe fences every implicated ref with `EXTERNAL_MUTATION_DETECTED`/`EFFECT_AMBIGUOUS`. No ordinary operation overlaps a live ticket. Tickets are deliberately private migration protocol—not V1 client methods—and are deleted family by family. The controller never merely observes an uncoordinated legacy write and calls that convergence.

**Gate**

Legacy and canonical state outputs agree on characterized cases; controller restart preserves identity/attention rules; no poller process; hook downtime/spool recovery passes; layout snapshot restore tested on throwaway server. The dedicated orderlies socket and M3b transition-ticket race/failure sentinels pass before any unowned family is allowed to remain.

**Rollback**

Stop admitting tickets and normal mutations, drain or fence every live ticket, snapshot, stop controller, then restore direct hook/poller launch and old `cockpit-state`. Leave new DB/stamps untouched. Legacy family scripts are re-enabled only after controller authority is down; no execution path needs reverse translation.

## Phase 4 — mutation authority by family

Migrate in this order, one independently switchable family per change:

1. spawn new + resume existing (`cockpit-spawn`, `cockpit-send`, Santa shim);
2. recover/reboot + retarget;
3. typed interactions (nudge, pause, compact, resume/replay) and `cockpit-cont` replacement by local-only `interaction.continue_process`; the replacement accepts no PID or signal, requires the exact pane generation/version plus current PID/start-ticks/process-group fingerprint, performs one fixed continue, and never repeats automatically;
4. move + soft pane remove/undo;
5. workspace rename/soft-close/undo and durable timers;
6. idle parking/swap/remaining layout operations, or explicitly retire them if value does not justify multi-target complexity;
7. launcher build/restore/kill through maintenance operations.

For each family:

- compatibility scripts parse legacy argv, resolve display target once through a read query, present stable expectations, and call V1;
- script stdout may include the old `%pane_id` for old consumers during overlap, but structured output includes `paneRef`; controller operations never accept `%pane_id` back as identity;
- owned-family direct writers refuse when controller is ready; tests search/process-trace to prove no raw tmux mutation call remains in their path;
- tmux bindings call `cockpitctl`; local navigation-only binds may stay direct if explicitly listed and identity-neutral;
- legacy and new clients race through the same controller in acceptance tests.

**Gate per family**

Required race/crash/idempotency sentinels, compatibility contract tests and a rollback drill. Advance only after no unexplained external-mutation event in dogfood.

**Rollback per family**

Flip that family to its prior compatibility implementation only after stopping/draining the controller family and snapshotting. Do not run both. If new stable refs have been returned externally, retain controller read/translation service even while the mutation family rolls back.

## Phase 5 — thin MCP and web surfaces

**Changes**

- Add stdio MCP as a pure V1 translator with honest read/write annotations and capability-driven tool publication.
- Replace `cockpit-webd` script `execFile` orchestration with the restricted controller client and event push.
- Require Access JWT signature/issuer/audience/expiry validation, verified identity allowlist, CSRF/origin/rate limits; keep tunnel/gateway loopback.
- Remove all web endpoints that accept old pane/session prefix targets. UI holds stable refs/version tuples and refreshes conflicts.

**Gate**

MCP cancellation watcher leak sentinel; no tool exposes raw key/tmux/exec; forged Access email without valid JWT fails; unauthorized capture/control fails; same operation via local/MCP/web returns the same error/result code.

**Rollback**

Disable MCP/gateway mutations and retain read-only state if safe. Re-enable old web binary only for operation families whose controller compatibility shims remain and only behind the same verified Access middleware; otherwise web becomes unavailable rather than direct-writing.

## Phase 6 — Orbital connector and identity migration

**Changes**

- Add a Cockpit connector using V1 state/events/capability negotiation.
- New goal-Cockpit launches call `pane.spawn` with Orbital execution ID as external correlation/idempotency basis and persist `{kind:'cockpit', paneRef, generationAtLaunch}`. Adopted provider session remains enrichment.
- New recover calls exact `paneRef` with current expected tuple. Steering remains capability-absent in V1.
- Stop timer export through `cockpit-state -> sessions.json` after the connector is proven; a compatibility fleet projection may remain generated from canonical state for other readers.
- Existing G15 `{paneId,name}` providers migrate only by an explicit idempotent Orbital correlation event/sidecar: accept a mapping when current controller state has exactly one pane matching the legacy pane ID and derived Orbital name and the operator confirms the batch. Ambiguous/missing mappings stay visible and cannot control. Never match by label alone.
- Preserve Orbital’s request-before-foreign-write, command intent digest, no silent route fallback, unattached immunity and manual interrupted-launch confirmation.

**Gate**

Crash after Cockpit success before Orbital records launch reconciles from the same controller idempotency key/paneRef without second spawn. Unauthorized/foreign pane recover fails. Legacy mapped and new executions coexist. Runner route tests are unchanged.

**Rollback**

Feature-switch Orbital back to the old adapter only for still-legacy providers and only while Cockpit compatibility shims exist. New paneRef-based providers remain read-only/recovery-required under old Orbital; never translate them to a guessed `%pane_id`. The additive mapping records remain.

## Phase 7 — enforce and retire

**Changes**

- Set `authority-mode=enforced`: every ordinary Cockpit tmux mutation must carry a running/prepared controller operation.
- Retire poller PID logic, duplicate state/classification, web script spawning, background reapers and legacy pending queues.
- Keep only narrow argv-compatible shims that call V1, local attach/navigation utilities and the fenced break-glass helper.
- Update operational docs and install/supervise only after all prior gates; service installation is outside this design exercise.

**Gate**

Repository static test denies tmux mutation verbs outside `internal/driver/tmux`, tests and explicit local attach/break-glass files. A multi-process soak across CLI/MCP/web/Orbital passes without external mutation. Break-glass exclusion/restart drill passes.

**Rollback**

Roll back the binary/schema reader to the immediately prior compatible release; keep DB/snapshots and fence semantics. If protocol/data compatibility is not guaranteed, stop ordinary mutation and use break-glass/read-only recovery—never reinstall many direct writers as an emergency shortcut.

## Explicit retirement list

| Current logic | Final disposition |
|---|---|
| `cockpit-poller` process, PID scan and state/layout loop | removed; controller observers/projector/snapshot |
| direct tmux writes in `cockpit`, `lib.sh`, `cockpit-pick`, `-send`, `-spawn`, `-reboot`, `-move`, `-pane`, `-ws`, `-toggle-idle`, `-cont`, hook | removed or private driver; names may remain as thin clients temporarily |
| `cockpit-state` reconstruction | thin V1 compatibility query, then optional retirement |
| `cockpit-webd` `execFile` calls to Cockpit scripts | removed; restricted V1 client |
| raw delayed `setsid/nohup` reapers | removed; durable controller timers |
| `/tmp/cockpit-pending.*` and tmux global undo slots as authority | removed; operations/idempotency/recoverable timers |
| `shim/wt.exe` command-string parsing | removed after Santa direct CLI integration |
| helper’s normal raw `claude-pane.sh` steering | superseded by MCP/CLI; retain only external documentation/legacy emergency use, not installed core |
| Orbital `cockpit-state` timer and `cockpit-spawn`/`cockpit-reboot` allowlist | replaced by connector after cutover; old keys retained only for rollback window |
| cwd-keyed Runner gate override | discard; optional typed observer later |
