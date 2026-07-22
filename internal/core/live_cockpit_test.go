package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is the live admission path exercised against a fresh equivalent tmux
// server. The test never invokes the named Cockpit server.
func TestLiveCockpitAdmissionParityOnEquivalentSocket(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "control.sock")
	registry := filepath.Join(root, "clients.json")
	credential := "live-equivalent-mcp"
	b, err := json.Marshal(map[string]any{"version": 1, "clients": []any{map[string]any{
		"credential": credential, "clientId": "cockpit-mcp", "profile": "mcp-local",
		"capabilities": []string{"state:read", "operations:read", "events:wait", "capture:sanitized", "interaction:nudge", "interaction:pause", "interaction:compact", "interaction:resume"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registry, b, 0600); err != nil {
		t.Fatal(err)
	}
	publicRegistry := filepath.Join(root, "public-clients.json")
	if err = os.WriteFile(publicRegistry, b, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = NewLiveCockpitDaemon(root, socket, publicRegistry); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("live constructor consulted Cockpit before rejecting public credentials: %v", err)
	}
	if _, err = NewDaemonWithCredentials(root, socket, "cockpit", registry); err == nil || !strings.Contains(err.Error(), "refusing live") {
		t.Fatalf("ordinary constructor admitted cockpit: %v", err)
	}
	auth, err := loadAuthenticator(registry)
	if err != nil {
		t.Fatal(err)
	}
	grant, ok := auth.verify(credential, "cockpit-mcp", "mcp-local")
	if !ok || has(grant.Capabilities, "metadata:write") || !has(grant.Capabilities, "interaction:nudge") {
		t.Fatalf("live credential grant is not bound to its profile/capabilities: %#v", grant)
	}
	if _, ok := auth.verify(credential, "other-client", "mcp-local"); ok {
		t.Fatal("live credential was not bound to its exact client")
	}

	tmuxSocket := fmt.Sprintf("cp-live-equivalent-%d", time.Now().UnixNano())
	if tmuxSocket == "cockpit" {
		t.Fatal("test selected the live socket")
	}
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "live-equivalent", "sleep 600")
	defer func() { _ = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run() }()

	d, err := newDaemon(root, socket, tmuxSocket, auth, true)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			d.Close()
		}
	}()
	call := func(method string, params any) any {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		result, err := d.dispatch(context.Background(), grant.Profile, grant.ClientID, grant.Capabilities, grant.ClientID != "", rpcRequest{Method: method, Params: raw})
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return result
	}

	list := call("state.snapshot", map[string]any{}).(map[string]any)
	panes := list["panes"].([]any)
	if len(panes) != 1 {
		t.Fatalf("list parity expected one stamped pane: %#v", list)
	}
	p, err := d.st.pane(panes[0].(map[string]any)["paneRef"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if p.Ref == "" || p.PaneID == "" || p.Generation < 1 || p.Version < 1 {
		t.Fatalf("stable pane identity missing: %#v", p)
	}
	fingerprint, err := d.tm.globalOption("@cockpit_server_fingerprint")
	if err != nil || fingerprint == "" {
		t.Fatalf("durable server fingerprint missing: %q %v", fingerprint, err)
	}

	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", p.PaneID, "@agent", "claude")
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", p.PaneID, "@state", "idle")
	status := call("pane.status", map[string]any{"paneRef": p.Ref}).(map[string]any)
	if status["provider"] != "claude" || status["observedState"] != "waiting" || !has(status["capabilities"].([]string), "interaction:nudge") {
		t.Fatalf("status parity lost controller-read state/capability facts: %#v", status)
	}
	capture := call("pane.capture", map[string]any{"paneRef": p.Ref, "lines": 10}).(map[string]any)
	if capture["paneRef"] != p.Ref || capture["redacted"] != true {
		t.Fatalf("capture parity lost stable/private result: %#v", capture)
	}
	wait := call("wait.for_change", map[string]any{"paneRef": p.Ref, "afterVersion": p.Version - 1, "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}).(map[string]any)
	if wait["paneRef"] != p.Ref || wait["matched"] != true {
		t.Fatalf("wait parity lost stable pane result: %#v", wait)
	}

	// A resident controller must inventory panes created after startup without
	// a restart. This is the same live-mode discovery path, on a random socket.
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "new-window", "-d", "-t", "live-equivalent", "-n", "later", "sleep 600")
	list = call("state.snapshot", map[string]any{}).(map[string]any)
	panes = list["panes"].([]any)
	if len(panes) != 2 {
		t.Fatalf("dynamic list parity expected two stamped panes: %#v", list)
	}
	seenLater := false
	for _, raw := range panes {
		candidate := raw.(map[string]any)
		if candidate["locator"].(map[string]any)["windowId"] != p.WindowID && candidate["paneRef"] != "" && candidate["generation"].(int64) >= 1 && candidate["resourceVersion"].(int64) >= 1 {
			seenLater = true
		}
	}
	if !seenLater {
		t.Fatalf("dynamic pane did not receive a stable identity: %#v", list)
	}

	interact := func(action, state, text string, n int) {
		t.Helper()
		p, err = d.st.pane(p.Ref)
		if err != nil {
			t.Fatal(err)
		}
		result := call("interaction."+action, map[string]any{
			"protocol": "1.0", "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339), "idempotencyKey": fmt.Sprintf("ik_%d_%032x", time.Now().Unix(), n), "paneRef": p.Ref, "text": text,
			"expectations": []any{map[string]any{"kind": "pane", "paneRef": p.Ref, "generation": p.Generation, "resourceVersion": p.Version, "material": map[string]any{"lifecycle": "active", "observedState": state}}},
		}).(map[string]any)
		if result["status"] != "effect-delivered-unconfirmed" {
			t.Fatalf("%s parity result: %#v", action, result)
		}
	}
	nudgeText := "live-equivalent nudge literal"
	interact("nudge", "waiting", nudgeText, 1)
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", p.PaneID, "@state", "paused")
	resumeText := "live-equivalent resume literal"
	interact("resume", "paused", resumeText, 2)
	trace, err := os.ReadFile(filepath.Join(root, "driver.trace"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trace), nudgeText) || strings.Contains(string(trace), resumeText) || strings.Contains(string(trace), "send-keys") || strings.Count(string(trace), "text=[REDACTED]") != 2 {
		t.Fatalf("live-capable trace leaked interaction text or argv: %q", trace)
	}

	d.Close()
	closed = true
	freshRoot := t.TempDir()
	if _, err = newDaemon(freshRoot, filepath.Join(freshRoot, "control.sock"), tmuxSocket, auth, true); err == nil || !strings.Contains(err.Error(), "CONTROLLER_NOT_READY") {
		t.Fatalf("fresh root adopted live-equivalent fingerprint: %v", err)
	}
}

func runLiveEquivalent(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
}
