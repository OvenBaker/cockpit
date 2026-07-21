# Independent architecture review record

## Review scope and independence

One fresh bounded reviewer (`/root/independent_arch_review`) reviewed the complete draft in this workspace after internal assembly. The reviewer was asked to find contradictions, missing safety contracts and false-pass paths, with emphasis on the brief's hard invariants. The reviewer made no file edits, did not inspect or mutate live Cockpit state, and no second reviewer or Fable was used.

Initial verdict: **not review-ready**—two blockers and five major findings. This record preserves that verdict and the disposition of every blocker/major; it is not rewritten as a clean first pass.

## Blockers and resolutions

### B1 — the wire contract could not express multi-resource execution-time CAS

Finding: the prose required pane/workspace/session lock sets, while the draft TypeScript exposed one singular mixed `ExpectedTarget`. Move, spawn, resume, close and global operations could not state or validate exact affected resources.

Resolution:

- `protocol-v1.types.ts` now has discriminated pane, workspace, workspace-member, session-uniqueness and projection expectations plus a non-empty list.
- Method parameter types encode exact tuples for pane-local calls, move, spawn, resume, retarget and workspace close/undo; maintenance encodes projection plus any scoped resource.
- `04-protocol-v1.md` specifies exact coverage and rejects missing, duplicate, unrelated or misordered expectations.
- `03-architecture-and-sequences.md` requires all claims to be released on dynamic membership change and forbids recomputation while holding any old claim. Observer updates use the same resource serialization boundary.

Disposition: **resolved**. The contract now carries the facts the scheduler and execution-time CAS must prove.

### B2 — the required pane-index race could pass by safely mutating

Finding: the original sentinel targeted A, removed B and allowed either safe mutation or conflict. That did not prove stale rejection after reindex and combined a separate poisoned-locator concern.

Resolution:

- R1 now creates A `.0` and intended B `.1`, records B's tuple, removes A, requires the locator observer to increment B's version when B becomes `.0`, then submits B's old tuple.
- The only pass is `CONFLICT_VERSION`, zero mutation argv and unchanged B/all-pane sentinels.
- R1b separately corrupts the client’s cached diagnostic locator, proves the valid serialized mutation contains no locator field, and requires the stable-ref effect to touch only B.
- R2 separately proves delete/recreate cannot inherit identity.

Disposition: **resolved**. `03-architecture-and-sequences.md` and `08-acceptance-matrix.md` now use the same unambiguous oracle.

## Major findings and resolutions

### M1 — protocol/types were incomplete and request identity was duplicated

Finding: events unsubscribe, cancellation/notifications, most query shapes and hook publication were absent; `RequestMeta.requestId` duplicated JSON-RPC `id`; operation results were untyped.

Resolution: the TypeScript surface now exhaustively maps every V1 request and result, includes `rpc.cancel` and `controller.event` notifications, `events.unsubscribe`, `attention.next`, hook request/result, typed operation-result variants and one request identity (the JSON-RPC envelope ID). Hook ingestion is explicitly `session.open` + `hook.publish` on the control socket, with the same envelope used for spool recovery.

Disposition: **resolved**.

### M2 — Phase 3 allowed uncontrolled legacy writers and left orderlies undecided

Finding: “detect and reconcile” could permit a legacy tmux effect to race the controller; `orderlies-up` had no selected disposition.

Resolution: the migration selects a dedicated `tmux -L orderlies` server and blocks Phase 3 until isolation is traced. Remaining legacy mutation families require a private transition ticket: exact resources are reserved by the controller, overlapping normal operations block, an allocated nonce is stamped into the effect, and completion is probed before release. Timeout, nonce mismatch, membership drift or ambiguity fences the resources. M3b provides the false-pass-resistant gate.

Disposition: **resolved**.

### M3 — implementation gates preceded the components they exercised

Finding: Slice 0 claimed executable transport gates without a daemon; Slice 4 claimed remove/move race tests before those families or the general scheduler existed.

Resolution: Slice 0 now gates static contracts/fixtures/characterization only. P1–P4 begin in Slice 1. Slice 4 first builds the general sorted all-or-none multi-resource scheduler and gates spawn/resume plus scheduler primitives. R1/R1b/R2 and opposite-direction move C1 gate Slice 6, where remove/move exist.

Disposition: **resolved**.

### M4 — idempotency retention contradicted indefinite replay wording

Finding: a pruned 30-day key could be mistaken for a new request and duplicate an old effect.

Resolution: V1 keys are `ik_<unix-seconds>_<128-bit-random>`, cannot be over five minutes in the future, and have an immutable 30-day admission/replay horizon. An old key always returns `IDEMPOTENCY_EXPIRED`, whether its row exists or has been pruned. R5b proves both cases and zero admission/effect.

Disposition: **resolved**.

### M5 — `cockpit-cont` had an inventory disposition but no coherent protocol operation

Finding: the migration promised a typed replacement but the method/capability/semantics were absent.

Resolution: `interaction.continue_process` is now local-only. It requires exact pane generation/version plus exact PID/start-ticks/process-group fingerprint, accepts neither PID selection nor signal input, performs one fixed continue, and requires post-action process/provider evidence. It is never automatically repeated. The protocol, type map, capability matrix, persistence probe, migration and O6b acceptance row agree.

Disposition: **resolved**.

## Minor observations also closed

- `OperationView.result` is a discriminated union rather than an unbounded generic record.
- `attention.next` is defined as a pure query.
- Literal-input tmux buffers have exact named deletion and bounded startup orphan cleanup.
- Hook ingress has one selected endpoint and authentication model.

## Post-resolution architect check before closure review

All blocker and major dispositions were checked against the revised files and the TypeScript contract compiled under strict no-emit checking. After review, Orbital main advanced from `937270e` to `99c3a59`; a read-only diff/status refresh found only Choir voice/recording/API/UI changes and no change to the inspected Cockpit integration seams, so the architecture findings remain applicable. This was an architect disposition and explicitly did not claim independent closure.

## Narrow Terra High closure pass

At the owner's request, one additional narrow, read-only Codex CLI review covered only B1, B2 and M1–M5. Its runtime header reported `model: gpt-5.6-terra`, `reasoning effort: high`, `sandbox: read-only`, session `019f8394-5362-7312-8fa4-553710b35030`. The response's requested self-description incorrectly said generic GPT-5/effort not reported; the invocation/runtime header above is the evidence for the selected model and effort. No files were edited by the reviewer.

Closure result:

| Finding | Terra verdict | Evidence/result |
|---|---|---|
| B1 | CLOSED | discriminated exact expectation tuples, exact coverage validation and all-claims release were coherent |
| B2 | OPEN | R1 was strong, but the then-current R1b attempted to send a diagnostic `displayTarget` that no valid V1 mutation can carry, so that oracle was not executable |
| M1 | CLOSED | exhaustive request/result/notification maps and hook endpoint were coherent |
| M2 | CLOSED | orderlies isolation, transition tickets and M3b closed the second-writer paths |
| M3 | CLOSED | slice gates now follow the components that implement them |
| M4 | CLOSED | time-bearing keys and R5b prevent post-pruning duplicate admission |
| M5 | CLOSED | the local-only fixed continue operation is coherent across type/protocol/migration/test surfaces |

Overall reviewer verdict: **CLOSURE FAIL**, solely because B2/R1b was not representable.

### B2 correction after the closure pass

The package now preserves the locator-free mutation wire and makes R1b executable at the correct boundary:

- `03-architecture-and-sequences.md` corrupts the client's cached `PaneView.locator.displayTarget`, then builds a normal mutation from B's current stable tuple.
- `08-acceptance-matrix.md` requires the recorded serialized frame to contain no locator/display target, exactly B to change in the drift-free case, the cached-locator pane to remain untouched, and the driver trace to name only B's current tmux pane ID.
- R1 remains the mandatory stale reindex rejection; R2 remains delete/recreate identity protection.

This resolves the reviewer's exact representability objection without granting diagnostic locators protocol authority. An attempted continuation of the same ephemeral reviewer session failed with `no rollout found`; no second Terra reviewer was launched. At this point B1 and M1–M5 had independent closure and corrected B2 awaited the appropriate independent architecture closure below.

## Opus High B2 closure

The orchestrator used Cockpit to create one sibling reviewer pane in the same `cp-core` workspace: `cockpit:13.1`. Before submission, its runtime UI was verified as Claude Opus 4.8, high effort, plan mode, rooted at `/home/gareth/workspaces/lean/cp-core`. The prompt was limited to corrected B2 and prohibited edits, external-repository inspection, live-tmux inspection, broader review and implementation. The pane returned idle after the review and made no file edits.

Independent decision: **B2 CLOSED / CLOSURE PASS**.

The reviewer found:

- `TmuxLocator.displayTarget` exists only on `PaneView`, a query/result shape; `MutationMeta`, `PaneExpectation` and `PaneTargetParams` cannot carry a locator.
- Corrected R1b is executable through the real client boundary: poison the cached `PaneView.locator.displayTarget`, build from B's stable tuple, schema-decode the recorded frame to prove locator absence, require exactly B to change, and require the poison-named pane to remain unchanged.
- R1 still requires committed reindex/version drift followed by exactly `CONFLICT_VERSION` and zero mutation argv; R2 separately covers delete/recreate identity.
- Unique process, scrollback and option sentinels plus the driver trace prevent generic failure, no-op or wrong-target false passes.

Combined closure status:

| Finding | Independent closure |
|---|---|
| B1 | CLOSED — Terra High narrow pass |
| B2 | CLOSED — Opus High B2-only pass after correction |
| M1–M5 | CLOSED — Terra High narrow pass |

The full material finding set is independently closed. The design package is at a **clean review-ready checkpoint**. For implementation, the owner clarified the role split: a Cockpit-spawned Terra High sibling pane is the writer; the lead architect and/or a separate Opus High pane review. This review exercise did not begin implementation.
