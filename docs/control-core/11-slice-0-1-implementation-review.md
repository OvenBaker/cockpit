# Slice 0/1 implementation and review record

## Checkpoint decision

**Decision: PASS.** Cockpit control-core Slice 0 and the badge-only Slice 1 vertical slice are complete at an isolated, uncommitted implementation checkpoint. The same independent Opus High reviewer that issued the implementation `BLOCK` performed the closure pass and returned `PASS` with **zero blockers and zero majors**.

This is evidence to begin from later; it is not authorization to touch the live Cockpit tmux server or install a controller.

## Scope and provenance

| Item | Value |
|---|---|
| Cockpit worktree | `implementation/`, branch `feat/cockpit-control-core-v1` |
| Cockpit base | clean `main` at `20f75ea8a1e5cd4e88ffa9da59b52b543c015263` |
| Orderlies worktree | `orderlies-implementation/`, branch `feat/orderlies-controller-domain` |
| Orderlies base | `5445f74050e8c7485711508517eb5253c6185630` |
| Implementation writer | the existing Cockpit sibling Terra High pane |
| Independent reviewer | the same existing Opus 4.8 High pane used for the initial implementation review |
| Runtime boundary | temporary roots and random `cp-it-*` tmux sockets only |
| Repository state | both implementation worktrees deliberately uncommitted |

No Fable or second implementation reviewer was used. The reviewer was read-only and edited no files.

## Implemented checkpoint

Slice 0 freezes and characterizes the executable V1 contract and moves the real Orderlies fleet onto a validated, dedicated tmux controller domain. Slice 1 implements the difficult controller spine as a Go binary with pure-Go SQLite:

- a server-scoped, nonblocking lease acquired before store migration or adoption;
- stable pane/workspace identities and durable tmux stamps;
- one credential-bound capability profile per connection;
- strict length-framed JSON-RPC, request IDs, cancellation and bounded outstanding work;
- per-pane serialization, queue-head CAS and one atomic tmux badge/version plan;
- idempotency, durable prepared/completed/recovery-required operations and compact audit;
- restart reconciliation, effect probing and fail-closed pane fencing;
- bounded event replay, cursor-gap/overflow resync and one-shot waits;
- static tmux mutation boundary and build-tagged fault injection;
- false-pass-resistant process, persistence, trace, event and non-target oracles.

The detailed executable claims and test names are in [../../SLICE-0-1-EVIDENCE.md](../../SLICE-0-1-EVIDENCE.md).

## Initial independent implementation review

The independent Opus reviewer initially returned **BLOCK**. The material findings were preserved rather than rewritten as a clean first pass:

| Finding | Initial problem | Closure |
|---|---|---|
| B1 | The Orderlies test exercised a fake contract while real scripts still targeted Cockpit. | All six real scripts source `controller-domain.sh`; real bootstrap, flock and stale-PID behavior run on a random dedicated socket. |
| M1 | No enforceable static boundary constrained production tmux mutations. | `tests/tmux-mutation-boundary.sh` gates the allowlisted driver seam. |
| M2 | Cursor-gap, overflow-resync and bounded subscription behavior were absent. | A bounded event ring, replay/gap/overflow semantics, global cap and deterministic tests were added. |
| M3 | Deterministic fault controls could be compiled into production. | Hooks are behind `cockpit_test`; the normal binary is scanned for every hook symbol. |

The review also identified weaker protocol and false-pass assertions. Rune-aware badge length, strict envelopes, standard/domain error framing, exact R3 trace/version/event assertions and a genuine two-pane non-target oracle were added.

## Architect audit and remediation

After the initial remediation, the lead audit rejected several superficially green paths and required stronger evidence:

- Two separate daemon roots now contend as OS processes for the same canonical tmux-server lease. Exactly one becomes authenticated and health-ready; the loser exits within a bounded deadline without creating a store or driver trace. A third root and a precreated empty database fail during read-only fingerprint preflight.
- Listener cleanup is tied to the exact recorded device/inode and cannot unlink a foreign replacement.
- Driver ambiguity, readback ambiguity and post-effect database failure enter one atomic recovery-required/fence transition before notification. Successful recovery records the original authenticated caller.
- Operation failure and recovery waiters are registered before the transition and receive exactly one wake.
- Subscription ownership is connection-scoped; a foreign unsubscribe is denied without disrupting delivery, and owner release/connection close returns resources to baseline.
- Session-open fields and profiles, pre/post-auth error taxonomy, invalid `id:null`, duplicate IDs, required mutation fields and the 64-request bound are asserted at the wire boundary.
- R3/O11 prove one winning badge, one durable and tmux version increment, one atomic trace line, one event, zero effect for each rejected case, and an unchanged uniquely identifiable non-target pane.

The lead's first independent rerun caught one real false-pass in `tests/orderlies-isolation.sh`: the heartbeat source oracle searched for unescaped JSON while the shell source necessarily contains escaped quotes. Terra changed it to a fixed-string source match that never invokes the network post. The corrected Orderlies gate then passed three times under Terra and three times under the lead.

## Verification evidence

Terra and the lead used separate Go caches. The lead-owned final gate produced:

| Gate | Result |
|---|---|
| `CGO_ENABLED=0 GOPROXY=off ... go test ./... -count=3` | PASS; `internal/core` `24.060s` |
| `go vet ./...` | PASS |
| `go test -race ./internal/core -count=1` | PASS; `11.580s` |
| sensitive lease/events/protocol/recovery/acceptance selection, `-count=3` | PASS; `18.929s` |
| corrected real Orderlies isolation, three consecutive runs | PASS |
| protocol, Bash characterization and tmux mutation-boundary scripts | PASS |
| strict TypeScript no-emit compile | PASS |
| production Go build plus fault-hook symbol scan | PASS; no forbidden symbols |
| shell syntax and both `git diff --check` gates | PASS |

Every tmux integration test creates and destroys a random throwaway server. The literal tmux socket name `cockpit` is rejected in the daemon and checked in driver traces.

Final hygiene found 846 unbound `cp-it-*` Unix socket inodes and 601 matching test lease files left in `/tmp/tmux-1000` after the repeated sandboxed runs. Process and socket-owner checks showed no tmux server, controller daemon or listener behind them. The lead removed only that exact disposable namespace and verified the remaining count was zero; these test artifacts are not recoverable or needed. This is a test-environment cleanup limitation, not live Cockpit state.

During diagnosis of the original Orderlies assertion failure, one direct call supplied the unsupported `--check-controller-domain` argument to `orderlies-heartbeat`; the script entered its normal heartbeat loop before the sandbox terminated it. Networking was restricted, no successful post was observed, and the final process check was empty. The corrected acceptance test and the independent reviewer never invoke the heartbeat command.

## Same-reviewer independent closure

After compaction of its original review context, the same Opus High pane performed a narrow read-only closure review. It inspected the actual daemon, store, tmux driver, model, build-tag split, Orderlies controller-domain scripts, and the lease, events, recovery, protocol and acceptance tests. It did not merely accept the evidence prose.

Final verdict: **PASS — zero blockers, zero majors.** The reviewer explicitly found that every original material item was resolved with an oracle capable of failing for the intended defect. It edited no files, ran no heartbeat post, and did not use the live Cockpit socket.

The reviewer recorded these non-gating limitations:

- Stable references are cryptographically random 128-bit values rather than the plan's UUIDv7 representation; uniqueness is preserved, ordering is not.
- Slice 1 has test-root/test-only credentials and does not implement production peer-UID/credential provisioning.
- Resource-limit rejection currently reuses `CAPABILITY_ABSENT`.
- Invalid mutation deadlines and invalid wait deadlines use slightly different error classes.
- A concurrent same-idempotency-key request on different panes could surface the SQLite uniqueness failure as `INTERNAL`; the 128-bit key suffix makes accidental occurrence negligible, but later slices should normalize this path.
- Two Orderlies poller helper calls use the intended deployed `$HOME/tools/orderlies` path rather than the worktree-relative path; no deployment or installation was performed here.

## Stop boundary and rollback posture

This checkpoint deliberately does **not** include provider observers, capture, spawn/resume, general multi-resource mutations, MCP/web/Orbital clients, break-glass execution, a service unit, live migration or layout cutover. Nothing was committed, pushed, merged, deployed or installed.

Before any live-socket work, preserve the approved stop gate: review this evidence, deliberately choose how to checkpoint the two branches, and perform a separate owner-authorized live-readiness exercise. The badge-only slice remains reversible because all runtime proof used disposable tmux servers and no production state.
