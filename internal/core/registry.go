package core

import "strings"

// MethodSpec is the one small, versioned common surface used by both ctl and
// MCP.  Transports may name a tool differently, but they never get their own
// controller operation or capability decision.
type MethodSpec struct {
	Method      string
	Capability  string
	ReadOnly    bool
	MCPTool     string
	Description string
	Destructive bool
	OpenWorld   bool
}

var methodRegistry = []MethodSpec{
	{"state.snapshot", "state:read", true, "list_panes", "List Cockpit panes with stable identities and current versions.", false, false},
	{"pane.status", "state:read", true, "get_status", "Get one pane's controller-read provider, observed state, and effective capabilities.", false, false},
	{"pane.capture", "capture:sanitized", true, "capture_pane", "Read a bounded, redacted tail from one pane.", false, false},
	{"wait.for_change", "events:wait", true, "wait_for_state", "Wait for a pane version or operation terminal transition.", false, false},
	{"capabilities.get", "state:read", true, "get_capabilities", "Get the effective controller capabilities.", false, false},
	// Slice 1 supports a badge, not a title or observed-state write. Keep it in
	// the common controller registry for ctl compatibility but do not advertise
	// a misleading MCP title/state tool.
	{"metadata.set_display", "metadata:write", false, "", "Set bounded display metadata with CAS.", false, false},
	{"interaction.nudge", "interaction:nudge", false, "nudge", "Deliver bounded literal guidance to a verified waiting agent pane.", false, true},
	{"interaction.pause", "interaction:pause", false, "pause", "Request one typed interrupt for a verified active agent pane.", false, false},
	{"interaction.compact", "interaction:compact", false, "compact", "Request a provider compaction with explicit evidence only.", false, false},
	{"interaction.resume", "interaction:resume", false, "resume", "Deliver bounded literal resume guidance to a recorded paused pane.", false, true},
	// Coordination vertical: structured task -> handoff -> exact-SHA review ->
	// acceptance cycle. Every operation is a typed controller mutation or
	// bounded read; none of them touches tmux or terminal output.
	{"coordination.workstream_create", "coord:admin", false, "", "Create a coordination workstream from a strict contract with server-side role bindings.", false, false},
	{"coordination.task_publish", "coord:write", false, "coord_task_publish", "Publish an immutable task assignment (or its single correction) into a workstream.", false, false},
	{"coordination.task_deliver", "coord:write", false, "", "Deliver a published task pointer through the provider-native seeded prompt-file capability.", false, false},
	{"coordination.task_acknowledge", "coord:write", false, "coord_task_acknowledge", "Record structured receipt of a delivered task pointer bound by request id and artifact hash.", false, false},
	{"coordination.task_claim", "coord:write", false, "coord_task_claim", "Claim an assigned task and acquire the sole exclusive builder write lease.", false, false},
	{"coordination.artifact_publish", "coord:write", false, "coord_artifact_publish", "Publish a strict supporting record into the immutable content-addressed store.", false, false},
	{"coordination.artifact_read", "coord:read", true, "coord_artifact_read", "Read one immutable coordination record by exact SHA-256.", false, false},
	{"coordination.handoff_submit", "coord:write", false, "coord_handoff_submit", "Submit a builder handoff binding one exact committed head SHA and output hashes.", false, false},
	{"coordination.review_request", "coord:write", false, "coord_review_request", "Request an exact-SHA review pinned to one builder handoff.", false, false},
	{"coordination.review_submit", "coord:write", false, "coord_review_submit", "Submit the single structured review verdict for the exact requested head.", false, false},
	{"coordination.lease_transfer", "coord:write", false, "", "Record an orchestrator-approved scoped small-fix lease transfer to the reviewer.", false, false},
	{"coordination.lease_return", "coord:write", false, "", "Return a transferred scoped write lease before any review verdict.", false, false},
	{"coordination.acceptance_submit", "coord:write", false, "coord_acceptance_submit", "Submit final acceptance binding the reviewed head, handoff, and review result.", false, false},
	{"coordination.release_submit", "coord:write", false, "coord_release_submit", "Submit the release handoff for an accepted task; merge and deploy stay external.", false, false},
	{"coordination.checkpoint_emit", "coord:write", false, "", "Emit a structural workspace checkpoint from the durable projection only.", false, false},
	{"coordination.status_get", "coord:read", true, "coord_status", "Read the compact current coordination projection for one workstream.", false, false},
	{"coordination.events_list", "coord:read", true, "coord_events", "List bounded, cursor-ordered coordination events.", false, false},
	{"coordination.wait", "coord:read", true, "coord_wait", "One-shot bounded wait for the next coordination event past a cursor.", false, false},
}

func MethodSpecs() []MethodSpec { return append([]MethodSpec(nil), methodRegistry...) }

func specForMethod(method string) (MethodSpec, bool) {
	for _, s := range methodRegistry {
		if s.Method == method {
			return s, true
		}
	}
	return MethodSpec{}, false
}

func MCPTools() []map[string]any {
	out := make([]map[string]any, 0, len(methodRegistry))
	for _, s := range methodRegistry {
		if s.MCPTool == "" {
			continue
		}
		ann := map[string]any{"readOnlyHint": s.ReadOnly, "destructiveHint": s.Destructive, "openWorldHint": s.OpenWorld}
		out = append(out, map[string]any{"name": s.MCPTool, "description": s.Description, "inputSchema": mcpSchema(s.Method), "annotations": ann})
	}
	return out
}

func mcpSchema(method string) map[string]any {
	if strings.HasPrefix(method, "coordination.") {
		return coordSchema(method)
	}
	target := map[string]any{"paneRef": map[string]any{"type": "string"}, "locator": map[string]any{"type": "string", "description": "Canonical session:window.pane convenience locator."}}
	switch method {
	case "state.snapshot", "capabilities.get":
		return map[string]any{"type": "object", "additionalProperties": false}
	case "pane.inspect", "pane.status":
		return map[string]any{"type": "object", "properties": target, "anyOf": []any{map[string]any{"required": []string{"paneRef"}}, map[string]any{"required": []string{"locator"}}}, "additionalProperties": false}
	case "pane.capture":
		target["lines"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200}
		return map[string]any{"type": "object", "properties": target, "required": []string{"lines"}, "additionalProperties": false}
	case "wait.for_change":
		target["operationRef"] = map[string]any{"type": "string"}
		target["afterVersion"] = map[string]any{"type": "integer", "minimum": 0}
		target["deadline"] = map[string]any{"type": "string"}
		return map[string]any{"type": "object", "properties": target, "required": []string{"afterVersion", "deadline"}, "additionalProperties": false}
	default:
		target["protocol"] = map[string]any{"const": "1.0"}
		target["deadline"] = map[string]any{"type": "string"}
		target["idempotencyKey"] = map[string]any{"type": "string"}
		target["expectations"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 1}
		if method == "interaction.nudge" || method == "interaction.resume" {
			target["text"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 16384}
		}
		return map[string]any{"type": "object", "properties": target, "required": []string{"protocol", "deadline", "idempotencyKey", "expectations"}, "additionalProperties": false}
	}
}

// coordSchema mirrors the strict controller-side envelopes. Full record
// validation (unknown-field rejection, bounds, hashes) is a controller
// decision; these schemas only describe the transport shape.
func coordSchema(method string) map[string]any {
	ws := map[string]any{"workstreamId": map[string]any{"type": "string"}}
	mutation := func(extra map[string]any) map[string]any {
		props := map[string]any{
			"workstreamId":     map[string]any{"type": "string"},
			"expectedRevision": map[string]any{"type": "integer", "minimum": 0},
			"idempotencyKey":   map[string]any{"type": "string"},
		}
		required := []string{"workstreamId", "expectedRevision", "idempotencyKey"}
		for k, v := range extra {
			props[k] = v
			required = append(required, k)
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	record := map[string]any{"type": "object"}
	switch method {
	case "coordination.task_publish", "coordination.artifact_publish", "coordination.handoff_submit", "coordination.review_request", "coordination.review_submit", "coordination.acceptance_submit", "coordination.release_submit":
		return mutation(map[string]any{"record": record})
	case "coordination.task_claim":
		return mutation(map[string]any{"taskId": map[string]any{"type": "string"}, "taskRevision": map[string]any{"type": "integer", "minimum": 0}})
	case "coordination.task_acknowledge":
		return mutation(map[string]any{"taskId": map[string]any{"type": "string"}, "taskRevision": map[string]any{"type": "integer", "minimum": 0}, "requestId": map[string]any{"type": "string"}, "artifactSha256": map[string]any{"type": "string"}})
	case "coordination.artifact_read":
		ws["sha256"] = map[string]any{"type": "string"}
		return map[string]any{"type": "object", "properties": ws, "required": []string{"workstreamId", "sha256"}, "additionalProperties": false}
	case "coordination.status_get":
		return map[string]any{"type": "object", "properties": ws, "required": []string{"workstreamId"}, "additionalProperties": false}
	case "coordination.events_list":
		ws["afterSeq"] = map[string]any{"type": "integer", "minimum": 0}
		ws["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200}
		return map[string]any{"type": "object", "properties": ws, "required": []string{"workstreamId", "afterSeq"}, "additionalProperties": false}
	case "coordination.wait":
		ws["afterSeq"] = map[string]any{"type": "integer", "minimum": 0}
		ws["deadline"] = map[string]any{"type": "string"}
		return map[string]any{"type": "object", "properties": ws, "required": []string{"workstreamId", "afterSeq", "deadline"}, "additionalProperties": false}
	}
	return map[string]any{"type": "object"}
}

func MCPMethod(tool string) (string, bool) {
	for _, s := range methodRegistry {
		if s.MCPTool == tool {
			return s.Method, true
		}
	}
	return "", false
}
