package core

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
	{"pane.inspect", "state:read", true, "get_status", "Get one pane by stable paneRef.", false, false},
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
}

func MethodSpecs() []MethodSpec { return append([]MethodSpec(nil), methodRegistry...) }

func specForMethod(method string) (MethodSpec, bool) {
	for _, s := range methodRegistry {
		if s.MCPTool == "" {
			continue
		}
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
		out = append(out, map[string]any{"name": s.MCPTool, "description": s.Description, "inputSchema": map[string]any{"type": "object", "additionalProperties": true}, "annotations": ann})
	}
	return out
}

func MCPMethod(tool string) (string, bool) {
	for _, s := range methodRegistry {
		if s.MCPTool == tool {
			return s.Method, true
		}
	}
	return "", false
}
