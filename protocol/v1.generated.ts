/* Generated from protocol/v1.schema.json for Slice 0/1 wire checking. */
export type Capability = "state:read" | "operations:read" | "events:wait" | "capture:sanitized" | "metadata:write" | "interaction:nudge" | "interaction:pause" | "interaction:compact" | "interaction:resume";
export type ErrorCode =
  | "INVALID_REQUEST" | "UNSUPPORTED_PROTOCOL" | "FRAME_TOO_LARGE" | "UNAUTHENTICATED"
  | "PERMISSION_DENIED" | "CAPABILITY_ABSENT" | "TARGET_NOT_FOUND" | "TARGET_GONE"
  | "CONFLICT_VERSION" | "CONFLICT_GENERATION" | "CONFLICT_MATERIAL_STATE"
  | "IDEMPOTENCY_CONFLICT" | "IDEMPOTENCY_EXPIRED" | "DEADLINE_EXCEEDED" | "CANCELLED"
  | "CONTROLLER_NOT_READY" | "INTERNAL";
export interface PaneExpectation { kind: "pane"; paneRef: `cpp_${string}`; generation: number; resourceVersion: number; material: { lifecycle: "active"; observedState?: "waiting" | "working" | "paused" } }
export interface BadgeRequest { protocol: "1.0"; deadline: string; idempotencyKey: `ik_${number}_${string}`; paneRef: `cpp_${string}`; badge: string; expectations: [PaneExpectation] }
export interface SessionOpenParams { protocol: "1.0"; clientId: string; claimedProfile: "local-operator" | "read-only" | "tmux-binding" | "mcp-local" | "web-gateway" | "orbital" | "hook-producer"; credential: string }
export interface PaneInspectParams { paneRef: `cpp_${string}` }
export interface OperationGetParams { operationRef: `cpo_${string}` }
export interface EventSubscribeParams { controllerEpoch?: `cpe_${string}`; afterEventSeq?: number; paneRef?: `cpp_${string}`; operationRef?: `cpo_${string}` }
export interface EventUnsubscribeParams { subscriptionRef: `cps_${string}` }
export interface WaitForChangeParams { paneRef?: `cpp_${string}`; operationRef?: `cpo_${string}`; afterVersion: number; deadline: string }
export interface RpcCancelParams { requestId: string | number }
export interface CaptureParams { paneRef: `cpp_${string}`; lines: number }
export interface InteractionRequest { protocol: "1.0"; deadline: string; idempotencyKey: `ik_${number}_${string}`; paneRef: `cpp_${string}`; text?: string; expectations: [PaneExpectation] }
export type EmptyParams = Record<string, never>;
export interface MethodParams {
  "session.open": SessionOpenParams;
  "controller.health": EmptyParams;
  "state.snapshot": EmptyParams;
  "capabilities.get": EmptyParams;
  "pane.inspect": PaneInspectParams;
  "pane.resolve": { canonical: string };
  "pane.capture": CaptureParams;
  "operation.get": OperationGetParams;
  "metadata.set_display": BadgeRequest;
  "interaction.nudge": InteractionRequest;
  "interaction.pause": InteractionRequest;
  "interaction.compact": InteractionRequest;
  "interaction.resume": InteractionRequest;
  "events.subscribe": EventSubscribeParams;
  "events.unsubscribe": EventUnsubscribeParams;
  "wait.for_change": WaitForChangeParams;
  "rpc.cancel": RpcCancelParams;
}
export type SliceMethod = keyof MethodParams;
export type JsonRpcRequest<M extends SliceMethod = SliceMethod> = { jsonrpc: "2.0"; id: string | number; method: M; params: MethodParams[M] };
