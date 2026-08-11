/*
 * Cockpit Control Protocol v1 — review contract.
 * JSON Schema 2020-12 is canonical in implementation; this exhaustive TypeScript
 * surface fixes the intended wire shape. It is not controller implementation.
 */

export type ProtocolVersion = "1.0";
export type ISO8601 = string;
export type ControllerEpoch = string;
export type WorkspaceRef = `cpw_${string}`;
export type PaneRef = `cpp_${string}`;
export type OperationRef = `cpo_${string}`;
export type IdempotencyKey = `ik_${number}_${string}`;
export type AgentSessionRef = { provider: "claude" | "codex"; id: string };

export type Capability =
  | "state:read" | "capture:sanitized" | "capture:unredacted"
  | "sessions:read" | "operations:read" | "events:wait"
  | "pane:spawn" | "session:resume" | "pane:retarget" | "pane:recover"
  | "pane:move" | "pane:soft-remove" | "pane:undo-remove"
  | "workspace:rename" | "workspace:soft-close" | "workspace:undo-close"
  | "interaction:nudge" | "interaction:pause" | "interaction:compact"
  | "interaction:resume" | "interaction:replay" | "interaction:continue-process"
  | "metadata:write" | "navigation:focus"
  | "maintenance:snapshot" | "maintenance:reconcile"
  | "break-glass:prepare" | "hook:publish";

export type ClientProfile =
  | "local-operator" | "tmux-binding" | "mcp-local"
  | "web-gateway" | "orbital" | "hook-producer";

export interface SessionOpenParams {
  protocol: ProtocolVersion;
  clientId: string;
  claimedProfile: ClientProfile;
  credential: string;
}
export interface SessionOpenResult {
  protocol: ProtocolVersion;
  controllerEpoch: ControllerEpoch;
  clientId: string;
  profile: ClientProfile;
  capabilities: Capability[];
  ready: boolean;
}

/* The JSON-RPC envelope id is the request identity; it is not repeated here. */
export interface RequestMeta { protocol: ProtocolVersion; deadline: ISO8601 }

export type ObservedExecutionState =
  | "starting" | "working" | "waiting" | "paused"
  | "stopped" | "failed" | "unknown";
export type AttentionState =
  | "none" | "just-finished" | "needs-input" | "waiting-gate" | "degraded";
export type SourceHealthState = "healthy" | "stale" | "unavailable" | "invalid";

export interface PaneExpectation {
  kind: "pane";
  paneRef: PaneRef;
  generation: number;
  resourceVersion: number;
  material: {
    lifecycle: "active" | "soft-removed" | "removed" | "quarantined";
    workspaceRef?: WorkspaceRef | null;
    agentSessionRef?: AgentSessionRef | null;
    observedStateIn?: ObservedExecutionState[];
    processFingerprint?: ProcessFingerprint;
  };
}
export interface WorkspaceExpectation {
  kind: "workspace";
  workspaceRef: WorkspaceRef;
  generation: number;
  resourceVersion: number;
  material: {
    lifecycle: "active" | "soft-closed" | "removed";
    memberDigest?: string;
  };
}
export interface WorkspaceMembersExpectation {
  kind: "workspace";
  workspaceRef: WorkspaceRef;
  generation: number;
  resourceVersion: number;
  material: {
    lifecycle: "active" | "soft-closed" | "removed";
    memberDigest: string;
  };
}
export interface SessionUniquenessExpectation {
  kind: "session-uniqueness";
  session: AgentSessionRef;
  mustBe: "not-live" | "live-in-pane";
  paneRef?: PaneRef;
}
export interface SessionNotLiveExpectation {
  kind: "session-uniqueness";
  session: AgentSessionRef;
  mustBe: "not-live";
}
export interface ProjectionExpectation {
  kind: "projection";
  controllerEpoch: ControllerEpoch;
  projectionVersion: number;
}
export type ResourceExpectation =
  | PaneExpectation | WorkspaceExpectation | WorkspaceMembersExpectation
  | SessionUniquenessExpectation | SessionNotLiveExpectation | ProjectionExpectation;
export type NonEmptyExpectations =
  [ResourceExpectation, ...ResourceExpectation[]];

export interface MutationMeta<E extends NonEmptyExpectations = NonEmptyExpectations> extends RequestMeta {
  idempotencyKey: IdempotencyKey;
  expectations: E;
}

export interface TmuxLocator {
  serverFingerprint: string;
  windowId: string;
  paneId: string;
  displayTarget: string; // diagnostic/navigation only
}
export interface ProcessFingerprint {
  pid: number;
  startTicks: number;
  processGroupId: number;
}
export interface SourceHealth {
  source: "tmux" | "process" | "claude-hook" | "claude-transcript" |
    "codex-transcript" | "runner";
  state: SourceHealthState;
  observedAt: ISO8601 | null;
  ageMs: number | null;
  code?: string;
}
export interface PaneView {
  paneRef: PaneRef;
  generation: number;
  resourceVersion: number;
  lifecycle: "active" | "soft-removed" | "removed" | "quarantined";
  workspaceRef: WorkspaceRef | null;
  locator: TmuxLocator | null;
  processFingerprint: ProcessFingerprint | null;
  agent: "claude" | "codex" | null;
  agentSessionRef: AgentSessionRef | null;
  cwd: string | null;
  observedState: ObservedExecutionState;
  attentionState: AttentionState;
  display: { label: string; badge: string | null };
  sourceHealth: SourceHealth[];
  capabilities: Capability[];
  activeOperationRef: OperationRef | null;
}
export interface WorkspaceView {
  workspaceRef: WorkspaceRef;
  generation: number;
  resourceVersion: number;
  lifecycle: "active" | "soft-closed" | "removed";
  name: string;
  tmuxWindowId: string | null;
  paneRefs: PaneRef[];
  memberDigest: string;
  attentionState: AttentionState;
  capabilities: Capability[];
}
export interface StateSnapshot {
  controllerEpoch: ControllerEpoch;
  eventSeq: number;
  projectionVersion: number;
  ready: boolean;
  degraded: boolean;
  workspaces: WorkspaceView[];
  panes: PaneView[];
}

export type OperationState =
  | "accepted" | "queued" | "running" | "completed"
  | "failed" | "cancelled" | "recovery-required";
export type OperationResult =
  | { kind: "none" }
  | { kind: "pane"; pane: PaneView }
  | { kind: "workspace"; workspace: WorkspaceView }
  | { kind: "display"; paneRef: PaneRef; generation: number; resourceVersion: number }
  | { kind: "interaction"; paneRef: PaneRef; evidenceDigest: string }
  | { kind: "snapshot"; snapshotId: string; digest: string }
  | { kind: "break-glass"; tokenRef: string; expiresAt: ISO8601 }
  | { kind: "cancel"; operationRef: OperationRef; cancellation: OperationView["cancellation"] };
export interface OperationView {
  operationRef: OperationRef;
  method: MutationMethod;
  callerId: string;
  targetRefs: Array<PaneRef | WorkspaceRef>;
  state: OperationState;
  acceptedAt: ISO8601;
  startedAt: ISO8601 | null;
  finishedAt: ISO8601 | null;
  effectCommitted: boolean;
  cancellation: "none" | "requested" | "before-effect" | "effect-continuing";
  result?: OperationResult;
  error?: CockpitError;
  evidenceDigest?: string;
}

export type QueryMethod =
  | "controller.health" | "capabilities.get" | "state.snapshot"
  | "workspace.inspect" | "pane.inspect" | "pane.capture"
  | "sessions.search" | "sessions.recent" | "sessions.recoverable"
  | "operation.get" | "operation.list" | "attention.next";
export type MutationMethod =
  | "pane.spawn" | "session.resume" | "pane.retarget" | "pane.move"
  | "pane.soft_remove" | "pane.undo_remove" | "pane.recover"
  | "workspace.rename" | "workspace.soft_close" | "workspace.undo_close"
  | "interaction.nudge" | "interaction.pause" | "interaction.compact"
  | "interaction.resume" | "interaction.replay" | "interaction.continue_process"
  | "metadata.set_display" | "navigation.focus"
  | "maintenance.snapshot" | "maintenance.reconcile"
  | "break_glass.prepare" | "operation.cancel";

export interface PaneSpawnParams extends MutationMeta<[WorkspaceExpectation]> {
  workspaceRef: WorkspaceRef;
  provider: "claude" | "codex";
  cwd: string;
  display: { label: string; badge?: string | null };
  remoteControl: boolean;
  correlation?: { externalSystem: "orbital"; externalExecutionId: string };
}
export interface SessionResumeParams extends MutationMeta<[WorkspaceExpectation, SessionNotLiveExpectation]> {
  workspaceRef: WorkspaceRef;
  session: AgentSessionRef;
  display?: { label?: string };
}
export interface PaneTargetParams extends MutationMeta<[PaneExpectation]> { paneRef: PaneRef }
export interface PaneRetargetParams extends MutationMeta<[PaneExpectation, SessionNotLiveExpectation]> {
  paneRef: PaneRef;
  session: AgentSessionRef;
}
export interface PaneMoveParams extends MutationMeta<[PaneExpectation, WorkspaceExpectation, WorkspaceExpectation]> {
  paneRef: PaneRef;
  destinationWorkspaceRef: WorkspaceRef;
}
export interface PaneUndoRemoveParams extends PaneTargetParams { removeOperationRef: OperationRef }
export interface WorkspaceTargetParams extends MutationMeta<[WorkspaceExpectation]> { workspaceRef: WorkspaceRef }
export interface WorkspaceRenameParams extends WorkspaceTargetParams { name: string }
export interface PrivateInstruction {
  text: string;
  contentType: "text/plain";
  externalModelTransmissionAcknowledged: true;
}
export interface InteractionInstructionParams extends PaneTargetParams { instruction: PrivateInstruction }
export interface InteractionReplayParams extends InteractionInstructionParams { priorOperationRef: OperationRef }
export interface ContinueProcessParams extends PaneTargetParams {
  processFingerprint: ProcessFingerprint;
  confirmation: "CONTINUE VERIFIED PANE PROCESS";
}
export interface DisplayMetadataParams extends PaneTargetParams { label?: string; badge?: string | null }
export interface NavigationFocusParams extends PaneTargetParams { clientTtyFingerprint: string }
export type PaneSoftRemoveParams = PaneTargetParams;
export type PaneRecoverParams = PaneTargetParams & { allowInterruptWorking?: boolean };
export type InteractionPauseParams = PaneTargetParams;
export type InteractionCompactParams = PaneTargetParams;
export interface WorkspaceSoftCloseParams extends MutationMeta<[WorkspaceMembersExpectation]> { workspaceRef: WorkspaceRef }
export interface WorkspaceUndoCloseParams extends MutationMeta<[WorkspaceMembersExpectation]> {
  workspaceRef: WorkspaceRef;
  closeOperationRef: OperationRef;
}
export type MaintenanceParams =
  (MutationMeta<[ProjectionExpectation]> & { scope: { kind: "global" } }) |
  (MutationMeta<[ProjectionExpectation, WorkspaceExpectation]> &
    { scope: { kind: "workspace"; workspaceRef: WorkspaceRef } }) |
  (MutationMeta<[ProjectionExpectation, PaneExpectation]> &
    { scope: { kind: "pane"; paneRef: PaneRef } });
export interface BreakGlassPrepareParams extends RequestMeta {
  idempotencyKey: IdempotencyKey;
  expectations: [ProjectionExpectation];
  confirmation: "PREPARE BREAK GLASS";
  controllingTtyFingerprint: string;
}
export interface OperationCancelParams extends RequestMeta { operationRef: OperationRef }

export interface EmptyParams extends RequestMeta {}
export interface StateSnapshotParams extends RequestMeta { refs?: Array<PaneRef | WorkspaceRef> }
export interface WorkspaceInspectParams extends RequestMeta { workspaceRef: WorkspaceRef }
export interface PaneInspectParams extends RequestMeta { paneRef: PaneRef }
export interface PaneCaptureParams extends RequestMeta {
  paneRef: PaneRef;
  tailLines: number;
  maxBytes: number;
  redaction: "strict" | "default" | "none";
}
export interface PaneCaptureResult {
  paneRef: PaneRef;
  generation: number;
  resourceVersion: number;
  capturedAt: ISO8601;
  text: string;
  truncated: boolean;
  controlsStripped: boolean;
  redactions: number;
  untrustedPrivateOutput: true;
}
export interface SessionQueryParams extends RequestMeta {
  provider?: AgentSessionRef["provider"];
  query?: string;
  limit: number;
}
export interface SessionSummary {
  session: AgentSessionRef;
  state: "live" | "recoverable" | "historical";
  paneRef?: PaneRef;
  observedAt: ISO8601;
}
export interface OperationGetParams extends RequestMeta { operationRef: OperationRef }
export interface OperationListParams extends RequestMeta { state?: OperationState; limit: number }
export interface AttentionNextParams extends RequestMeta {
  afterPaneRef?: PaneRef;
  states?: AttentionState[];
}
export interface AttentionNextResult { pane: PaneView | null }
export interface ControllerHealthResult {
  controllerEpoch: ControllerEpoch;
  ready: boolean;
  degraded: boolean;
  fenced: boolean;
  schemaVersion: number;
  sourceHealth: SourceHealth[];
}
export interface CapabilitiesResult {
  effective: Capability[];
  absent: Array<{ capability: Capability; reason: string }>;
}

export type WaitPredicate =
  | { kind: "resource-version-after"; paneRef: PaneRef; resourceVersion: number }
  | { kind: "generation-not-equal"; paneRef: PaneRef; generation: number }
  | { kind: "observed-state-in"; paneRef: PaneRef; states: ObservedExecutionState[] }
  | { kind: "attention-state-in"; paneRef: PaneRef; states: AttentionState[] }
  | { kind: "operation-terminal"; operationRef: OperationRef }
  | { kind: "controller-ready" };
export interface WaitForChangeParams extends RequestMeta {
  after: { controllerEpoch: ControllerEpoch; eventSeq: number } | null;
  predicate: WaitPredicate;
}
export interface WaitForChangeResult {
  outcome: "matched" | "deadline" | "cancelled" | "resync-required";
  cursor: { controllerEpoch: ControllerEpoch; eventSeq: number };
  matchedEvent?: ControllerEvent;
  snapshot?: StateSnapshot;
}
export interface EventSubscribeParams extends RequestMeta {
  after: { controllerEpoch: ControllerEpoch; eventSeq: number } | null;
  eventTypes?: ControllerEvent["type"][];
  refs?: Array<PaneRef | WorkspaceRef | OperationRef>;
}
export interface EventSubscribeResult {
  subscriptionId: string;
  cursor: { controllerEpoch: ControllerEpoch; eventSeq: number };
  outcome: "subscribed" | "resync-required";
  snapshot?: StateSnapshot;
}
export interface EventUnsubscribeParams extends RequestMeta { subscriptionId: string }
export interface EventUnsubscribeResult { removed: boolean }
export type ControllerEvent = {
  controllerEpoch: ControllerEpoch;
  eventSeq: number;
  occurredAt: ISO8601;
  type: "controller.lifecycle" | "topology.changed" | "pane.changed" |
    "workspace.changed" | "operation.changed" | "source-health.changed" |
    "capabilities.changed";
  refs: Array<PaneRef | WorkspaceRef | OperationRef>;
  resourceVersion?: number;
  summary: Record<string, unknown>;
};
export interface RpcCancelParams { requestId: string }
export interface ControllerEventNotificationParams { subscriptionId: string; event: ControllerEvent }
export interface HookPublishParams extends RequestMeta {
  eventId: string;
  provider: "claude" | "codex";
  eventKind: string;
  occurredAt: ISO8601;
  paneId: string;
  session?: AgentSessionRef;
  transcriptPathDigest?: string;
}
export interface HookPublishResult { accepted: boolean; duplicate: boolean }

export type ErrorCode =
  | "INVALID_REQUEST" | "UNSUPPORTED_PROTOCOL" | "FRAME_TOO_LARGE"
  | "UNAUTHENTICATED" | "PERMISSION_DENIED" | "CAPABILITY_ABSENT"
  | "TARGET_NOT_FOUND" | "TARGET_GONE" | "TARGET_QUARANTINED"
  | "CONFLICT_VERSION" | "CONFLICT_GENERATION" | "CONFLICT_MATERIAL_STATE"
  | "IDEMPOTENCY_CONFLICT" | "IDEMPOTENCY_EXPIRED"
  | "SESSION_ALREADY_LIVE" | "QUEUE_CONFLICT"
  | "PANE_OPERATOR_ACTIVE" | "PANE_COMPOSING"
  | "DEADLINE_EXCEEDED" | "CANCELLED" | "TIMEOUT_UNCONFIRMED"
  | "SOURCE_STALE" | "CONTROLLER_NOT_READY" | "EXTERNAL_MUTATION_DETECTED"
  | "EFFECT_AMBIGUOUS" | "BREAK_GLASS_ACTIVE" | "INTERNAL";
export interface CockpitError {
  code: ErrorCode;
  message: string;
  retryable: boolean;
  operationRef?: OperationRef;
  target?: {
    paneRef?: PaneRef;
    workspaceRef?: WorkspaceRef;
    actualGeneration?: number;
    actualResourceVersion?: number;
  };
  details?: Record<string, unknown>;
}

export interface MethodParams {
  "session.open": SessionOpenParams;
  "controller.health": EmptyParams;
  "capabilities.get": EmptyParams;
  "state.snapshot": StateSnapshotParams;
  "workspace.inspect": WorkspaceInspectParams;
  "pane.inspect": PaneInspectParams;
  "pane.capture": PaneCaptureParams;
  "sessions.search": SessionQueryParams;
  "sessions.recent": SessionQueryParams;
  "sessions.recoverable": SessionQueryParams;
  "operation.get": OperationGetParams;
  "operation.list": OperationListParams;
  "attention.next": AttentionNextParams;
  "wait.for_change": WaitForChangeParams;
  "events.subscribe": EventSubscribeParams;
  "events.unsubscribe": EventUnsubscribeParams;
  "pane.spawn": PaneSpawnParams;
  "session.resume": SessionResumeParams;
  "pane.retarget": PaneRetargetParams;
  "pane.move": PaneMoveParams;
  "pane.soft_remove": PaneSoftRemoveParams;
  "pane.undo_remove": PaneUndoRemoveParams;
  "pane.recover": PaneRecoverParams;
  "workspace.rename": WorkspaceRenameParams;
  "workspace.soft_close": WorkspaceSoftCloseParams;
  "workspace.undo_close": WorkspaceUndoCloseParams;
  "interaction.nudge": InteractionInstructionParams;
  "interaction.pause": InteractionPauseParams;
  "interaction.compact": InteractionCompactParams;
  "interaction.resume": InteractionInstructionParams;
  "interaction.replay": InteractionReplayParams;
  "interaction.continue_process": ContinueProcessParams;
  "metadata.set_display": DisplayMetadataParams;
  "navigation.focus": NavigationFocusParams;
  "maintenance.snapshot": MaintenanceParams;
  "maintenance.reconcile": MaintenanceParams;
  "break_glass.prepare": BreakGlassPrepareParams;
  "operation.cancel": OperationCancelParams;
  "hook.publish": HookPublishParams;
}
export interface MethodResults {
  "session.open": SessionOpenResult;
  "controller.health": ControllerHealthResult;
  "capabilities.get": CapabilitiesResult;
  "state.snapshot": StateSnapshot;
  "workspace.inspect": WorkspaceView;
  "pane.inspect": PaneView;
  "pane.capture": PaneCaptureResult;
  "sessions.search": SessionSummary[];
  "sessions.recent": SessionSummary[];
  "sessions.recoverable": SessionSummary[];
  "operation.get": OperationView;
  "operation.list": OperationView[];
  "attention.next": AttentionNextResult;
  "wait.for_change": WaitForChangeResult;
  "events.subscribe": EventSubscribeResult;
  "events.unsubscribe": EventUnsubscribeResult;
  "pane.spawn": OperationView;
  "session.resume": OperationView;
  "pane.retarget": OperationView;
  "pane.move": OperationView;
  "pane.soft_remove": OperationView;
  "pane.undo_remove": OperationView;
  "pane.recover": OperationView;
  "workspace.rename": OperationView;
  "workspace.soft_close": OperationView;
  "workspace.undo_close": OperationView;
  "interaction.nudge": OperationView;
  "interaction.pause": OperationView;
  "interaction.compact": OperationView;
  "interaction.resume": OperationView;
  "interaction.replay": OperationView;
  "interaction.continue_process": OperationView;
  "metadata.set_display": OperationView;
  "navigation.focus": OperationView;
  "maintenance.snapshot": OperationView;
  "maintenance.reconcile": OperationView;
  "break_glass.prepare": OperationView;
  "operation.cancel": OperationView;
  "hook.publish": HookPublishResult;
}
export interface NotificationParams {
  "rpc.cancel": RpcCancelParams;
  "controller.event": ControllerEventNotificationParams;
}
export interface JsonRpcRequest<M extends keyof MethodParams = keyof MethodParams> {
  jsonrpc: "2.0";
  id: string;
  method: M;
  params: MethodParams[M];
}
export interface JsonRpcNotification<M extends keyof NotificationParams = keyof NotificationParams> {
  jsonrpc: "2.0";
  method: M;
  params: NotificationParams[M];
}
export interface JsonRpcSuccess<M extends keyof MethodResults = keyof MethodResults> {
  jsonrpc: "2.0";
  id: string;
  result: MethodResults[M];
}
export interface JsonRpcErrorObject {
  code: number;
  message: string;
  data?: CockpitError;
}
export interface JsonRpcFailure { jsonrpc: "2.0"; id: string | null; error: JsonRpcErrorObject }
