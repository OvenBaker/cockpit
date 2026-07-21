# Current-state evidence and writer/consumer inventory

## Evidence boundary

Inspection was read-only. The source states used for this package are:

| Source | Inspected state | Notes |
|---|---|---|
| Cockpit | clean baseline `main` / `origin/main` at `20f75ea`; current worktree HEAD `cacaae7` on `feat/lean-runner-gate-badge` | worktree also has uncommitted changes to `cockpit` and `lib.sh`; no mutation performed |
| Cockpit feature delta | `cacaae7` changes only `cockpit-poller` versus `main`; uncommitted delta changes `cockpit` and `lib.sh` | gate badge reads a cwd-keyed Runner file; uncommitted work changes TSV layout persistence to JSON/history |
| Orbital | clean `main` / `origin/main` at `99c3a59` (advanced during the exercise from inspected `937270e`) | intervening 15-file delta is Choir voice/recording/API/UI only; Cockpit integration seams below are unchanged; no mutation performed |
| Orbital relevant worktrees | clean `g15/execution-routing` at `705f853`; clean `digest/queued-periodic-reconcile` at `363434e` | both are ancestors of current main and their integration is present there; all listed worktrees were status-checked, with unrelated owner edits only in `choir-g16` |
| Claude helper | `/home/gareth/.codex/skills/manage-claude-sessions/scripts/claude-pane.sh` | 2,569 bytes, mtime 2026-07-17, SHA-256 `adcdbb51ecb5a1494084fa6c602e53ee961a5a3b37a35e5ec694fd66c578ce3b` |
| Runtime evidence | Go `1.26.1` (`linux/amd64`, CGO enabled); Node `v22.17.1`; tmux `3.5a` | adjacent Lean Runner already uses Go + `database/sql` + `modernc.org/sqlite`; built-in `node:sqlite` exists but emits an experimental warning |

The clean Cockpit `main` is the migration base. The owner explicitly clarified that the lean-runner feature work is disposable. This package therefore recommends re-expressing any wanted gate signal through the new observer contract and discarding the old branch later, but it does not delete or change it.

## Material findings from code

1. `cockpit-poller` is a singleton only by process scan/PID file. It kills matching pollers, writes pane/window options, adopts transcripts, maintains an in-memory `just-finished` latch, retiles and persists layout. Its singleton does not fence other scripts.
2. Pane mutation is scattered. `cockpit-pick`, `cockpit-send`, `cockpit-spawn` and `cockpit-reboot` each resolve a target, issue `respawn-pane`/`split-window`, then stamp multiple options in separate calls. `cockpit-move`, `cockpit-pane`, `cockpit-toggle-idle` and `cockpit-ws` independently move or delete topology.
3. Clean-main persistence is a throttled atomic rename of a TSV snapshot. The uncommitted feature work improves the format/history but still does not make tmux effect plus persistence atomic.
4. Hooks directly stamp `@session_id`, `@hook_state` and `@hook_at` on tmux. They intentionally avoid teardown events overwriting a newer session, demonstrating that event ordering already matters.
5. `cockpit-state` independently reconstructs the web/Orbital projection from tmux options, so consumers can see a partial multi-call update.
6. `cockpit-webd` validates input and uses `execFile`, but each HTTP action shells out to a different script. Its optional email-header check does not itself validate the Access JWT at origin.
7. Orbital G15 correctly performs request-before-foreign-write and has command-id digest semantics, but its current provider identity `{paneId,name}` treats tmux `%pane_id` as a durable handle and the display label as the crash anchor. Both must migrate to Cockpit-issued refs.
8. Orbital reads `cockpit-state` on a timer, writes `sources/fleet/sessions.json`, then pure adapters correlate by pane id/name/session id. Steering is honestly absent; spawn and recover invoke `cockpit-spawn` and `cockpit-reboot` through an argv-only allowlist.
9. `claude-pane.sh` provides useful intervention policy, but accepts general tmux targets, classifies idle from a title prefix, sleeps/polls, returns raw capture, and uses direct `send-keys`. It has no stable identity, CAS, serialization, capability or crash record.

## Cockpit readers and writers on clean `main`

“Write” includes tmux topology/options/navigation, agent process signals, input injection and durable layout. Display-only UI commands are shown because they still need an explicit local capability, though they are lower risk than execution mutations.

| Component | Current reads | Current writes/effects | Ownership problem | Migration disposition |
|---|---|---|---|---|
| `cockpit` launcher | session existence, layout snapshot, transcript/session candidates | kill server, new session/window/panes, chrome/options/bindings, select layout/window, start poller, restore | boot, state, policy and tmux driver mixed; can kill active server | retain launcher UX; boot/restore become controller operations; bindings call `cockpitctl`; attach remains a local tmux navigation exception |
| `lib.sh` | tmux current window/panes; Santa DB; transcripts; process liveness | `cockpit_resolve_workspace` may create a window; snapshot/history helpers write files in dirty branch | “library” has hidden topology mutation; classifications duplicated by callers | split pure provider/domain helpers from controller-only driver; no client-side tmux helper |
| `cockpit-poller` | all panes/options, active pane/window, Santa DB, Claude/Codex transcripts, Runner badge on feature branch | adopts `@session_id`; writes `@state`, `@badge`, `@sc`, `@label`, workspace/global counts; retiles; layout snapshot | nearest controller but not a command authority; singleton is not a lease | subsume into `cockpitd` observers/projector/reconciler; retire process and PID scan |
| `cockpit-hook` | hook stdin, `$TMUX_PANE`, current `@session_id` | direct `@session_id`, `@hook_state`, `@hook_at` | untrusted global hook is a concurrent tmux writer | tiny nonblocking event client or private spool; controller validates provenance/freshness and projects state |
| `cockpit-state` | tmux windows/panes/options; recent cwd DB | none | second state reconstruction can observe partial writes | compatibility shim to `state.snapshot`; retire reconstruction |
| `cockpit-spawn` | grid/workspace/current pane/session option | create/reuse pane, launch agent/shell, stamp metadata, layout | external callers receive ephemeral `%pane_id`; effect/stamps are non-atomic | shim to `pane.spawn`; return `paneRef`, generation/version and optional display locator |
| `cockpit-send` | Santa/transcripts/process liveness; tmux session/panes | focus existing pane or create/reuse/resume/stamp/layout; cold pending file | races liveness and placement; pending file is another queue | shim to `session.resume`; controller owns duplicate-session guard and cold-start queue |
| `cockpit-pick` | candidates, current pane/window and options | retarget/respawn, split/kill, create window, stamp/layout | interactive UI contains full mutation implementation | retain picker only as UI; submit typed operations with captured expectations |
| `cockpit-reboot` | scans all panes/options/state | forced `respawn-pane`, stamp badge/session | target may be `%id`/session prefix; state check and mutation not atomic | shim to `pane.recover`; exact stable target and CAS; bulk mode expands under controller snapshot |
| `cockpit-move` | current topology and display names | `join-pane`, `break-pane`, layout/focus | classic resolve-then-renumber window | `pane.move` with pane + source/destination locks and stable refs |
| `cockpit-pane` plus reaper | panes/options/undo globals | soft remove via `break-pane`, graveyard metadata, delayed `kill-window`, undo join | detached background reaper is an independent later writer | controller durable timer + `pane.soft_remove`/`pane.undo_remove`; no orphan reaper process |
| `cockpit-ws` plus reaper | windows/options/undo globals | mark/rename/select, delayed `kill-window`, undo | same independent-timer race at workspace scope | durable operation/timer under workspace/global scheduler |
| `cockpit-toggle-idle` | pane states/topology | moves panes to/from `parked`, layout/global flag | state-derived multi-pane mutation with no global ordering | typed local-only layout operation; sorted multi-lock set or defer beyond initial protocol |
| `cockpit-next` | current pane and `@state` | selects pane/display message | navigation depends on poller projection | `attention.next` query + local `navigation.focus`; controller supplies canonical choice |
| `cockpit-cont` | pane PID | recursively sends `SIGCONT` | execution mutation bypasses state/operation log | typed `interaction.continue_process`, local-only and provider/process-identity checked; not raw signal API |
| `cockpit-related` | focused session option; Santa related | delegates to `cockpit-send` | target and resume separated across programs | query candidate then call `session.resume` with expectations |
| `cockpit-retarget` | none itself | execs `cockpit-pick retarget` | compatibility alias | keep as thin compatibility shim, then retire |
| `cockpit-undo` | global undo option | dispatches pane/workspace undo | one tmux global last-action slot | query controller’s recoverable operations; typed undo by `operationRef` |
| `cockpit-help` | none | local popup only | no execution risk, but raw client dependence | remain local UI or call `cockpitctl help`; outside mutation authority |
| `cockpit-search` | Santa DB/transcripts/process liveness | none | useful query but current DB concerns mixed into shell | typed `sessions.recent/search`; controller/provider catalogue owns validation |
| `shim/wt.exe` | Santa-generated argv text | delegates to `cockpit-send` | parses a command string to recover ID | make Santa invoke `cockpitctl session resume`; keep shim only during compatibility phase |
| `web/index.html` | HTTP state/search/recent | HTTP new/resume/reboot | client semantics coupled to script output strings | use typed web gateway responses and controller event stream |
| `cockpit-webd` | script JSON/stdout | `execFile` to state/spawn/reboot/search/send | a second orchestration surface; 15s process timeout unsuitable for long ops | restricted controller client; no Cockpit scripts; verify Access JWT; SSE/WebSocket maps controller events |
| `orderlies-up` reference | outside inspected scope | launcher can start it; orderlies own `aide` panes and stamp `@orderly` | known external owner can mutate same server | selected V1 disposition: isolate on `tmux -L orderlies` before controller state authority; a future restricted-client integration is optional |

Native tmux bindings set by `cockpit` also directly run `select-pane`, `resize-pane`, `previous/next-window`, `new-window`, swap operations and popup/menu commands. Navigation/zoom can remain a documented local interactive exception only if it cannot change Cockpit identity or execution. `new-window`, swap/move, remove and all execution-affecting bindings must route through `cockpitctl` before authority enforcement.

## External consumers and producers

| Consumer/producer | Current contract | Risk/gap | Target contract |
|---|---|---|---|
| Orbital source refresher `ops/refresh-sources.mjs` | invokes `cockpit-state`, normalizes panes, writes `sources/fleet/sessions.json` | polling lag; ephemeral pane id/name correlation; another JSON projection | restricted `state.snapshot` + `events.subscribe`; persist Cockpit `paneRef`/`agentSessionRef` in Orbital provider identity |
| Orbital execution adapter | invokes `cockpit-spawn`; binds `{kind:'cockpit',paneId,name}` | `%pane_id` is not durable; name is mutable display metadata | call `pane.spawn` with Orbital idempotency key; bind `{kind:'cockpit',paneRef}` plus generation; session is enrichment |
| Orbital recover control | invokes `cockpit-reboot` with adopted session id, pane id or name-derived handle | ambiguous/prefix target; capability is static code truth | call `pane.recover` on exact `paneRef` with expected generation/version; negotiate `capabilities.get` |
| Orbital execution projection/UI | observes all by pane/name/session; steer absent | matching any display label can attach incorrectly | match only controller-issued `paneRef`; keep steer absent until both sides advertise/grant a typed capability |
| Cloudflare Tunnel/Access | tunnel to loopback webd, email header allowlist | origin trusts header without validating assertion | gateway validates Access JWT issuer/audience/signature and identity, then uses restricted UDS token |
| Santa TUI | `SANTA_RESUME_CMD`/`wt.exe` shim to `cockpit-send` | string parsing and direct writer | supported CLI call to typed resume |
| Claude global hooks | execute `cockpit-hook` for every session | direct tmux metadata write from globally invoked producer | authenticated bounded hook event or atomic spool; no tmux access |
| Codex transcripts/notify | poller parses rollouts; notify path not wired | weaker needs-input signal and provider divergence | provider-specific observer with explicit source health; absent facts remain absent |
| Lean Runner gate badge feature | poller reads cwd-keyed JSON and overrides state | cwd collision, stale TTL, conflates execution/attention source | typed Runner observation keyed by stable run/execution correlation; separate observed source from attention projection |
| `manage-claude-sessions/claude-pane.sh` | general tmux target; status/watch/nudge/pause/compact/resume | index race, raw capture/input, title heuristic, polling and no idempotency | replace normal use with MCP/`cockpitctl`; retain only as documented legacy/break-glass inspiration, not core |

## Current ownership map to preserve

- Cockpit owns tmux topology, agent/pane/session correlation, observed provider state and supported control operations.
- Orbital owns portfolio routing, its execution ledger, request-before-foreign-write, and correlation to Cockpit’s returned identity. It must not own tmux semantics.
- Claude/Codex transcripts and hooks are observations, not Cockpit commands.
- Runner owns Runner run/gate state. Cockpit may project it only through a typed source with freshness.
- Display label/badge are metadata, never identity or execution-state assertions.
