# Orbital delivery operating contract v1

## Purpose

Deliver useful behaviour quickly from an owner-approved brief while preserving existing data and supported flows. Reliability work is proportionate to the feature's risk. Review exists to catch material defects, not to maximize theoretical completeness.

## Build-a-brief contract

An approved build-a-brief package is the builder's complete primary input. The builder should proceed without further owner guidance unless it finds a material contradiction or a decision whose alternatives would produce meaningfully different products.

The package must contain:

1. Problem and intended outcome.
2. User-visible flow and affected surfaces.
3. Explicit in-scope and out-of-scope boundaries.
4. Current deployed state and relevant repository locations.
5. Dependencies and integration order.
6. Decisions already made, including design/architecture selections where applicable.
7. Acceptance criteria stated as observable behaviour.
8. Data migration, compatibility, security, and deployment constraints only where materially relevant.
9. Known follow-ups and non-goals.
10. A review profile: ordinary, elevated-data-risk, security-sensitive, or migration-sensitive.

Optional prototype, microsite, design, and architecture stages enrich the same package. They do not create separate authorities or require the builder to rediscover settled decisions.

If information is absent but a reversible, conventional implementation choice exists, the builder records the assumption and proceeds. It asks only when the choice is material and not safely reversible.

## Roles

### Orchestrator

- Validate that the brief is build-ready; repair small omissions before delegation.
- Freeze scope and exact base revision.
- Give the builder the complete brief and repository context once.
- Broker milestone artifacts, not terminal transcripts.
- Do not edit implementation or perform shadow reviews.
- Triage surprises and reviewer findings against the brief.
- Produce the exact-SHA release handoff immediately after acceptance.

### Builder

- Implement the frozen brief directly.
- Make reasonable reversible decisions without requesting guidance.
- Run focused tests while building.
- Record material assumptions and genuine follow-ups.
- Return a committed exact-SHA handoff with changed behaviour, focused evidence, known limitations, and migration/deployment notes.

### Reviewer

- Review the exact SHA against the frozen brief and affected existing behaviour.
- Optimize for material user, data, security, and regression risk.
- Do not add requirements, demand generalized infrastructure, or turn hypothetical hardening into a release blocker.
- Fix a genuinely small defect directly when authorized; otherwise return a concise finding packet.
- Classify findings as blocker, material, or follow-up. Style preferences and speculative edge cases are follow-up or omitted.

### Release conductor

- Consume exact-SHA acceptance handoffs.
- Resolve integration order and actual conflicts.
- Run one proportionate consolidated gate immediately before deployment.
- Merge, deploy, verify the live user-visible flow, and report deployed truth.
- Do not duplicate implementation review.

## Scope control

The approved brief is a scope ceiling as well as a requirements floor.

- Implement the smallest coherent change that satisfies the observable acceptance criteria.
- Do not absorb adjacent cleanup, platform generalization, speculative future requirements, or unrelated defects into the workstream.
- An unexpected discovery expands scope only when the current feature cannot work safely without addressing it, or when the change would otherwise cause a concrete supported-flow, data, or security regression.
- Use the narrowest safe containment for such a discovery. Record the broader correction as a separate proposed workstream.
- Reviewer suggestions do not amend scope. Only the orchestrator may classify a finding as required, using the materiality test below.
- A dependency discovered during implementation is either satisfied by an already accepted interface, isolated as a separately sequenced workstream, or escalated. It is not silently rebuilt inside the current task.
- Time already spent is never justification for retaining expanded scope.

## Materiality test

A finding may block only when evidence shows at least one of:

- An explicit acceptance criterion is not met.
- A supported existing flow regresses.
- User data can be lost, corrupted, exposed, or materially misattributed.
- A realistic security boundary is crossed.
- The application cannot build, start, migrate, or perform the changed flow.

The following are normally non-blocking follow-ups:

- Generalization for hypothetical future consumers.
- Additional durability beyond the stated risk profile.
- Adversarial tamper cases without a realistic trust boundary.
- Refactors, naming preferences, stylistic consistency, or extra observability.
- Expanded compatibility not required by the brief.
- Tests that prove implementation internals rather than observable behaviour.

## Review circuit-breaker

There are at most two failed review rounds.

After a second failed round, all implementation and re-review activity stops. The orchestrator publishes an escalation triage containing:

1. The exact work SHA or uncommitted checkpoint.
2. Every unresolved finding and its evidence.
3. For each finding: `in-scope blocker`, `in-scope material`, `valid follow-up`, `irrelevant/nitpick`, or `reviewer error`.
4. Whether the brief was ambiguous or the implementation diverged.
5. A recommended disposition: one narrowly bounded repair, amend the brief, accept with follow-ups, split the work, or request owner judgment.

No agent may respond to a second failed review with another open-ended “keep fixing” instruction. Resumption requires an explicit orchestrator disposition; owner input is required only for a material product decision.

## Test cadence

- Builder: focused tests for changed behaviour.
- Reviewer: focused reproduction of material findings.
- Release conductor: one consolidated application/build/integration gate before deployment.
- Full suites are not repeated after every review correction unless a change invalidates prior evidence in a material way.

## Coordination

- Durable task, handoff, review, decision, acceptance, and release artifacts are semantic authority.
- Terminal panes are interaction and liveness surfaces only.
- No continuous pane scraping or token-burning watch loops.
- Agents wake on explicit milestones/events.
- Every artifact identifies workstream, revision, brief hash, base SHA, head SHA where applicable, author role, and predecessor artifact.

## Completion

A workstream is complete when its accepted exact SHA has been handed to release-conductor. Delivery is complete when release-conductor reports the deployed SHA and live verification. Post-deployment knowledge curation records only high-level deployed capability and meaningful limitations, not a changelog.
