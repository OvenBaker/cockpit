# Security and privacy threat model

## Scope, assets and trust boundaries

Assets:

- integrity of pane/session targeting and tmux topology;
- confidentiality of terminal capture, transcripts, working directories and model instructions;
- external-model capacity/context and the ability to interrupt or resume work;
- durable stable identities, idempotency records, snapshots and audit evidence;
- operator identity at the web boundary;
- availability of normal control and a safe recovery path.

Trust boundaries:

1. a dedicated Cockpit tmux socket versus every other tmux server;
2. private Go controller/driver versus local clients;
3. local stdio MCP and its model versus the controller;
4. Cloudflare Access/Tunnel and loopback web gateway versus the Unix socket;
5. Orbital service/domain versus Cockpit execution authority;
6. globally invoked hooks/transcripts/provider output versus the observer validator;
7. normal controller versus local break-glass;
8. memory-only private content versus durable state/audit.

Security assumes one Unix account is the administrative boundary. Mode `0600` and same-UID peer checks prevent other local users, not a hostile process already running as the operator. Profile tokens provide least privilege and confused-deputy protection between cooperating same-UID clients; they are not a sandbox against a same-UID compromise. Strong isolation would require a separate OS user and is out of scope.

## Threats and controls

| Threat | Applicable surface | Control and failure behavior |
|---|---|---|
| second normal controller after crash/stale socket | daemon lifecycle | kernel `flock` held for lifetime; only lock owner may remove verified stale socket; socket/PID file never grants authority |
| controller and break-glass mutate together | break-glass | same nonblocking lock plus durable active fence; controller stays fenced on abandoned break-glass record |
| wrong tmux server/session | every mutation | private driver hard-codes/allowlists the dedicated socket/session and verifies server fingerprint; protocol has no socket/session parameter |
| pane-index renumber targets another process | every pane operation | stable `paneRef`, execution-time generation/version/material CAS and tmux stamp check; display locator ignored for mutation |
| delete/recreate inherits authority | topology | tombstones and new refs; logical restore requires controller snapshot and generation bump |
| arbitrary command/key injection | MCP/web/Orbital/local | no public exec/argv/tmux/key method; driver has typed fixed builders; subprocesses use argv arrays and no shell; model text goes through a literal temporary tmux buffer/stdin path, then buffer is deleted |
| option/control-character injection | labels, cwd, instructions, captures | strict UTF-8 byte limits; reject NUL/C0/C1 in requests except allowed LF/TAB in instructions; schema unknown-field rejection; sanitize display strings; capture strips ANSI/control/bidi-dangerous formatting before response |
| duplicate model prompt or spawn | client retry/crash | caller-scoped idempotency HMAC and effect-intent probes; uncertainty never auto-retries |
| forged/stale hook changes state/session | hooks | same-UID producer credential, strict event allowlist/size, event-id dedupe, timestamp/freshness window, exact pane/session/provider correlation; invalid input degrades source health and cannot mutate tmux |
| transcript path traversal/symlink swap | observers | derive paths only from provider roots and verified IDs; open no-follow where available, check owner/regular file/root containment, bound tail reads; never trust a hook-provided arbitrary path |
| client asserts working/idle to hide attention | all clients | no state-setting operation; observed and attention states are pure projections from source facts |
| MCP confused deputy obtains break-glass/raw capture | MCP | profile-bound credential; no break-glass, unredacted capture, focus or generic shell; tool annotations accurately mark writes; capabilities rechecked server-side |
| terminal output prompt-injects a model | MCP capture | output is bounded/sanitized and explicitly marked `untrustedPrivateOutput`; MCP result separates data from tool instructions and warns not to obey pane text; no capture is automatically fed into mutation |
| private capture leaks to web/Orbital | web/Orbital | capability absent in V1; gateway/connector cannot request it even if UI/API is tampered |
| instruction/capture retained in logs | store/audit/errors | raw content memory-only and zeroed/released after use where practical; durable HMAC digest + byte count only; no stdout/stderr in restricted errors; structured logger redacts content fields |
| cwd/session identity leaks between clients | profiles | state projection is profile-filtered; Orbital sees allowed fleet/owned correlations; error shape avoids target enumeration |
| compromised Orbital controls foreign panes | Orbital | token grants spawn/recover only; recover requires paneRef returned/bound to that Orbital execution and matching correlation; no capture/retarget/steer V1 |
| forged Access email header at loopback origin | web | require `Cf-Access-Jwt-Assertion`; verify signature against the configured team JWKS, exact issuer and application audience; then apply email/identity allowlist. Do not trust the email header alone |
| stolen/expired Access assertion or key rotation failure | web | validate `exp`/`nbf`, issuer and audience per request; bounded cached JWKS with refresh on unknown `kid`; fail closed when verification/refresh cannot establish validity |
| CSRF/cross-origin browser write | web | SameSite secure session/CSRF token bound to validated Access identity, exact Origin/Host allowlist, no permissive CORS, JSON content type, POST-only writes, per-identity rate limits |
| remote reaches raw controller | web/network | controller binds Unix socket only; web binds loopback; tunnel targets gateway; filesystem credential never sent to browser |
| event/wait resource exhaustion | MCP/web/Orbital | per-connection/global watcher limits, typed predicates, bounded ring/events, deadline clamp, release on disconnect/cancel, rate limits |
| capture resource exhaustion | all capture clients | line and byte caps before/after sanitization, subprocess timeout, no full scrollback/transcript fallback |
| stale source permits unsafe interruption/resume | interactions | operation-specific freshness/material preconditions; return `SOURCE_STALE`; only pause may act on working state and still requires exact process/provider identity |
| unauthorized direct same-UID tmux write | legacy/manual | migration shims refuse when controller ready; topology/stamp digest detects drift and fences refs. Documented residual risk: same UID can bypass ordinary policy |
| malicious/corrupt durable DB/snapshot | startup | owner/mode/no-symlink checks, SQLite integrity/schema checks, signed/digested snapshot, fail closed and break-glass; never auto-delete |
| dependency/binary substitution | Go build/deploy | checked-in `go.mod/go.sum`, minimal direct dependencies, reproducible build metadata, binary path/owner checks in service, characterization/integration tests; no runtime download |

Cloudflare’s current official guidance says the origin receives the application token in `Cf-Access-Jwt-Assertion`, should verify its signature with the account’s published keys, and must validate the configured issuer and application AUD. The key set is published below the team domain and rotates, so refresh must be automatic and fail closed: [Cloudflare Access — Validate JWTs](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/).

## Authentication and authorization

### Local Unix clients

- The runtime directory/socket are owner-only and Linux peer credentials must match the daemon UID.
- Each cooperating client has a random 256-bit credential bound in the database/config to an immutable profile and client ID. Credentials are loaded from a `0600` file or inherited descriptor, never command line or environment passed to child tools.
- The server issues a connection session after `session.open`; subsequent payloads cannot change caller/profile.
- Credential rotation can overlap old/new tokens briefly, is audited, and never changes durable operation ownership.
- The controller authorizes method, target visibility/ownership and current effective capability independently of client-side UI.

### Local MCP

The stdio MCP is a thin protocol translator. It holds the `mcp-local` credential, publishes only methods backed by current capabilities, and contains no tmux logic. Read tools (`state`, `inspect`, bounded capture, capabilities, operations, wait) carry read-only annotations. Nudge/pause/compact/resume/spawn/recover/metadata are writes even when they seem harmless or pre-approved. Tool output never includes the UDS credential, private state DB path, controller stack trace or raw driver argv.

### Web gateway

The existing code’s optional `Cf-Access-Authenticated-User-Email` equality check is insufficient as an origin authenticator because a direct loopback caller can set it. The migrated gateway:

1. binds only loopback and accepts only the configured Host/Origin;
2. validates the Access JWT signature, `iss`, `aud`, validity window and algorithm using the configured team-domain JWKS;
3. applies the allowed identity list to verified claims;
4. maps that identity to the fixed `web-gateway` controller profile;
5. uses a gateway-held UDS credential never exposed to JavaScript;
6. enforces CSRF/rate/body limits and strips sensitive downstream errors;
7. pushes sanitized controller events by an authenticated SSE/WebSocket session with origin checks.

If JWT/JWKS configuration is absent or validation fails, gateway mutation and state access fail closed. The old “email check optional/off” mode is not allowed for a tunnel-exposed deployment.

### Orbital

Orbital authenticates as a service client. It can read allowed canonical state/events, spawn with its stable external execution correlation, set bounded display metadata on its own panes, and recover only a bound pane. It cannot capture, focus, retarget, remove, compact, pause, nudge, resume, break glass or reconcile in V1. Future steering requires a Cockpit protocol capability, an Orbital reviewed allowlist/capability update, and an explicit private-context policy; neither side infers it from a generic method.

### Hooks

Hook credentials grant only `hook:publish`. The hook opens a normal framed control-socket session authenticated to the `hook-producer` profile, but that profile can submit only the strict observation envelope; spool replay enters the identical validator. Hooks cannot query state or write metadata. A global hook firing outside Cockpit is a verified no-op. Spool filenames and payload IDs are controller-generated/validated, not used as paths supplied by hook JSON.

## Capture and redaction policy

Processing order:

1. capture exact pane ref after generation check with an argv-only fixed tmux capture command;
2. enforce raw subprocess time/byte limit;
3. decode UTF-8 with replacement, strip ANSI/OSC/DCS and all disallowed C0/C1 controls;
4. normalize line endings and cap lines;
5. apply redaction profile (known token/key formats, configured path/email/account patterns; `strict` also replaces long high-entropy strings and absolute home paths);
6. cap final bytes without splitting a code point and set `truncated`, `controlsStripped`, `redactions`;
7. return with `untrustedPrivateOutput=true` and never audit the text.

Redaction is defense in depth, not a guarantee that arbitrary terminal output contains no secret. That is why Web/Orbital receive no capture and MCP receives a small bounded default-redacted view only on explicit tool use.

## External-model write policy

| Operation | External/private effect | Required boundary classification |
|---|---|---|
| nudge/resume/replay | sends caller text and possibly existing provider context to Claude/Codex | write; explicit acknowledgement field; content digest only in audit |
| compact | asks provider to consume capacity and transform recoverable context | write; fixed command, explicit completion evidence |
| pause | interrupts a running model/tool turn | write; exact target/state and interrupt capability |
| recover/retarget | restarts provider and loads a conversation/context | write; session identity + CAS, even with no new prompt |
| capture | reveals private untrusted model output to caller/model | read but privacy-sensitive; bounded capability |
| wait | observes future state only | read-only; cancellation can never schedule a mutation |

## Break-glass security

Break-glass is a recovery exception, not a privileged network API. It requires local TTY confirmation, same exclusive lock, durable fence, pre-snapshot and compact before/after evidence. No web, MCP, Orbital or hook credential can prepare/enter it. An abandoned active record blocks normal mutation even after the helper PID disappears. Raw same-user tmux commands outside this helper remain unsupported and are treated as external mutation on the next reconciliation.
