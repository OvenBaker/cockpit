package core_test

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This is deliberately process-to-process: stdio MCP -> resident controller
// -> real throwaway tmux. It never uses the live cockpit socket.
func TestMCPVerticalProtocolAndCancellation(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	c := exec.Command(f.bin, "mcp-stdio", "--socket", f.socket)
	c.Env = mcpCredentialEnv(t, "test-mcp")
	in, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = c.Process.Kill(); _ = c.Wait() }()
	s := bufio.NewScanner(out)
	send := func(v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if _, e := in.Write(append(b, '\n')); e != nil {
			t.Fatal(e)
		}
	}
	recv := func() map[string]any {
		t.Helper()
		if !s.Scan() {
			t.Fatal("mcp closed")
		}
		var v map[string]any
		if e := json.Unmarshal(s.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}})
	if r := recv(); r["result"] == nil {
		t.Fatalf("initialize: %#v", r)
	}
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}})
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	tools := recv()["result"].(map[string]any)["tools"].([]any)
	seenRead, seenWrite := false, false
	for _, raw := range tools {
		tool := raw.(map[string]any)
		ann := tool["annotations"].(map[string]any)
		if tool["name"] == "capture_pane" && ann["readOnlyHint"] == true {
			seenRead = true
		}
		if tool["name"] == "nudge" && ann["readOnlyHint"] == false && ann["openWorldHint"] == true {
			seenWrite = true
		}
	}
	if !seenRead || !seenWrite {
		t.Fatalf("tool annotations missing read/write distinction: %#v", tools)
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "list_panes", "arguments": map[string]any{}}})
	if r := recv(); r["result"] == nil || r["result"].(map[string]any)["isError"] == true {
		t.Fatalf("list: %#v", r)
	}
	base := watcherCount(t, f)
	send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "wait_for_state", "arguments": map[string]any{"paneRef": f.pane["paneRef"], "afterVersion": f.pane["resourceVersion"], "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}}})
	waitForWatcherCount(t, f, base+1)
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 4, "reason": "test"}})
	r := recv()
	if !strings.Contains(r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string), "CANCELLED") {
		t.Fatalf("cancelled wait: %#v", r)
	}
	waitForWatcherCount(t, f, base)

	// An operation-only wait is legal V1 input. It must not be forced through
	// locator resolution, and cancellation must reach the same controller wait.
	f.refresh()
	hold := filepath.Join(f.root, "hold-after_prepared_commit")
	ack := filepath.Join(f.root, "barriers", "after_prepared_commit")
	if err := os.WriteFile(hold, []byte("hold"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(hold)
	mutation := f.async(f.badgeParams("operation-wait", ik(0, 2400), f.pane["resourceVersion"].(float64)))
	waitForFile(t, ack)
	db, err := sql.Open("sqlite", filepath.Join(f.root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var op string
	if err = db.QueryRow("SELECT ref FROM operations WHERE status='prepared' ORDER BY created_at DESC LIMIT 1").Scan(&op); err != nil {
		t.Fatal(err)
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{"name": "wait_for_state", "arguments": map[string]any{"operationRef": op, "afterVersion": 0, "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}}})
	waitForWatcherCount(t, f, base+1)
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 5, "reason": "test"}})
	r = recv()
	if !strings.Contains(r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string), "CANCELLED") {
		t.Fatalf("cancelled operation wait: %#v", r)
	}
	waitForWatcherCount(t, f, base)
	if err = os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	if out := <-mutation; !strings.Contains(string(out), "completed") {
		t.Fatalf("held mutation: %s", out)
	}
}

func TestMCPNudgeRaceUsesControllerCASAndPrivateAudit(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@agent", "claude")
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@state", "idle")
	f.refresh()
	before, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	secret := "literal guidance: TOKEN=never-persist-this"
	params := func(n int) map[string]any {
		return map[string]any{"protocol": "1.0", "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339), "idempotencyKey": ik(0, 1900+n), "paneRef": f.pane["paneRef"], "text": secret, "expectations": []any{map[string]any{"kind": "pane", "paneRef": f.pane["paneRef"], "generation": f.pane["generation"], "resourceVersion": f.pane["resourceVersion"], "material": map[string]any{"lifecycle": "active", "observedState": "waiting"}}}}
	}
	var wg sync.WaitGroup
	results := make([]map[string]any, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = mcpOneCall(t, f, "nudge", params(i)) }(i)
	}
	wg.Wait()
	wins, conflicts := 0, 0
	for _, r := range results {
		text := r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		if strings.Contains(text, "effect-delivered-unconfirmed") {
			wins++
		}
		if strings.Contains(text, "CONFLICT_VERSION") {
			conflicts++
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("mcp race=%#v", results)
	}
	after, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	delta := string(after[len(before):])
	if strings.Count(delta, "interaction.nudge text=[REDACTED]") != 1 || strings.Contains(delta, secret) {
		t.Fatalf("race trace was duplicate or leaked literal input: %q", delta)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow("SELECT count(*) FROM audit WHERE before_digest LIKE ? OR after_digest LIKE ?", "%never-persist-this%", "%never-persist-this%").Scan(&n); err != nil || n != 0 {
		t.Fatalf("private text leaked to audit n=%d err=%v", n, err)
	}
	var stored string
	if err = db.QueryRow("SELECT intent FROM operations WHERE method='interaction.nudge' LIMIT 1").Scan(&stored); err != nil || strings.Contains(stored, secret) {
		t.Fatalf("private text leaked to operation intent %q err=%v", stored, err)
	}
}

func TestMCPStatusFailsClosedAndLiteralTextSubmits(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@agent", "claude")
	before, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	status := mcpOneCall(t, f, "get_status", map[string]any{"paneRef": f.pane["paneRef"]})["result"].(map[string]any)["structuredContent"].(map[string]any)
	if status["provider"] != "claude" || status["observedState"] != "unknown" || mcpHasCapability(status, "interaction:nudge") {
		t.Fatalf("missing state failed open: %#v", status)
	}
	bad := interactionArgs(f, "still must not deliver", "waiting", 2500)
	if got := mcpOneCall(t, f, "nudge", bad); !strings.Contains(fmt.Sprint(got), "CONFLICT_MATERIAL_STATE") {
		t.Fatalf("unknown state nudge: %#v", got)
	}
	after, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	if strings.Contains(string(after[len(before):]), "send-keys") {
		t.Fatal("unknown observed state delivered terminal input")
	}
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@state", "needs-input")
	legacyAttention := mcpOneCall(t, f, "get_status", map[string]any{"paneRef": f.pane["paneRef"]})["result"].(map[string]any)["structuredContent"].(map[string]any)
	if legacyAttention["provider"] != "claude" || legacyAttention["observedState"] != "unknown" || mcpHasCapability(legacyAttention, "interaction:nudge") {
		t.Fatalf("legacy needs-input was admitted as an interaction state: %#v", legacyAttention)
	}

	output := filepath.Join(f.root, "literal-input")
	command := fmt.Sprintf("bash -c 'IFS= read -r line; printf %%s \"$line\" > %s; sleep 60'", output)
	run(t, "tmux", "-L", f.tmux, "respawn-pane", "-k", "-t", f.targetPane, command)
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@agent", "claude")
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@state", "idle")
	status = mcpOneCall(t, f, "get_status", map[string]any{"paneRef": f.pane["paneRef"]})["result"].(map[string]any)["structuredContent"].(map[string]any)
	if status["provider"] != "claude" || status["observedState"] != "waiting" || !mcpHasCapability(status, "interaction:nudge") {
		t.Fatalf("stamped status did not expose interaction facts: %#v", status)
	}
	text := "literal text survives $() and spaces"
	if got := mcpOneCall(t, f, "nudge", interactionArgs(f, text, "waiting", 2501)); !strings.Contains(fmt.Sprint(got), "effect-delivered-unconfirmed") {
		t.Fatalf("literal nudge: %#v", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(output); e == nil {
			if string(b) != text {
				t.Fatalf("literal input=%q", b)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("literal text was not submitted with Enter")
}

func interactionArgs(f *fixture, text, state string, n int) map[string]any {
	return map[string]any{"protocol": "1.0", "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339), "idempotencyKey": ik(0, n), "paneRef": f.pane["paneRef"], "text": text, "expectations": []any{map[string]any{"kind": "pane", "paneRef": f.pane["paneRef"], "generation": f.pane["generation"], "resourceVersion": f.pane["resourceVersion"], "material": map[string]any{"lifecycle": "active", "observedState": state}}}}
}
func mcpHasCapability(status map[string]any, want string) bool {
	for _, raw := range status["capabilities"].([]any) {
		if raw == want {
			return true
		}
	}
	return false
}

func TestMCPPrivateCredentialAndSessionDenial(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	missing := exec.Command(f.bin, "mcp-stdio", "--socket", f.socket)
	if err := missing.Run(); err == nil {
		t.Fatal("mcp accepted no private credential source")
	}
	publicPath := filepath.Join(t.TempDir(), "public-credential")
	if err := os.WriteFile(publicPath, []byte("test-mcp"), 0644); err != nil {
		t.Fatal(err)
	}
	public := exec.Command(f.bin, "mcp-stdio", "--socket", f.socket)
	public.Env = append(os.Environ(), "COCKPIT_MCP_CREDENTIAL_FILE="+publicPath)
	if err := public.Run(); err == nil {
		t.Fatal("mcp accepted a non-private credential file")
	}
	denied := exec.Command(f.bin, "mcp-stdio", "--socket", f.socket)
	denied.Env = mcpCredentialEnv(t, "wrong")
	if out, err := denied.CombinedOutput(); err == nil || strings.Contains(string(out), "tools") {
		t.Fatalf("denied session advertised tools: err=%v out=%s", err, out)
	}
}

func TestCredentialRegistryBindsProfileAndCapabilities(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	local := rawOpen(t, f.socket, map[string]any{"protocol": "1.0", "clientId": "cockpitctl", "claimedProfile": "local-operator", "credential": "test-local"})
	if local["result"] == nil || !mcpHasCapability(local["result"].(map[string]any), "metadata:write") {
		t.Fatalf("local grant: %#v", local)
	}
	mismatch := rawOpen(t, f.socket, map[string]any{"protocol": "1.0", "clientId": "cockpitctl", "claimedProfile": "read-only", "credential": "test-local"})
	if !strings.Contains(fmt.Sprint(mismatch), "UNAUTHENTICATED") {
		t.Fatalf("profile mismatch admitted: %#v", mismatch)
	}
	mcp := rawOpen(t, f.socket, map[string]any{"protocol": "1.0", "clientId": "cockpit-mcp", "claimedProfile": "mcp-local", "credential": "test-mcp"})
	if mcp["result"] == nil || mcpHasCapability(mcp["result"].(map[string]any), "metadata:write") || !mcpHasCapability(mcp["result"].(map[string]any), "interaction:nudge") {
		t.Fatalf("mcp capability binding: %#v", mcp)
	}
}

func TestControllerRejectsPublicCredentialRegistry(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	f.stop()
	if err := os.Chmod(f.credentials, 0644); err != nil {
		t.Fatal(err)
	}
	bad := exec.Command(f.bin, "daemon", "--test-root", f.root, "--socket", f.socket, "--tmux-socket", f.tmux, "--credentials-file", f.credentials)
	if out, err := bad.CombinedOutput(); err == nil || !strings.Contains(string(out), "private") {
		t.Fatalf("public credential registry admitted: %v %s", err, out)
	}
}

func TestMCPCutoverParitySmoke(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	ctlList := f.call("state.snapshot", map[string]any{})["result"].(map[string]any)
	mcpList := mcpOneCall(t, f, "list_panes", map[string]any{})["result"].(map[string]any)["structuredContent"].(map[string]any)
	if len(ctlList["panes"].([]any)) != len(mcpList["panes"].([]any)) {
		t.Fatalf("list parity ctl=%#v mcp=%#v", ctlList, mcpList)
	}
	t.Logf("list: panes=%d", len(mcpList["panes"].([]any)))
	ctlStatus := f.call("pane.status", map[string]any{"paneRef": f.pane["paneRef"]})["result"].(map[string]any)
	mcpStatus := mcpOneCall(t, f, "get_status", map[string]any{"paneRef": f.pane["paneRef"]})["result"].(map[string]any)["structuredContent"].(map[string]any)
	if ctlStatus["paneRef"] != mcpStatus["paneRef"] || ctlStatus["resourceVersion"] != mcpStatus["resourceVersion"] {
		t.Fatalf("status parity ctl=%#v mcp=%#v", ctlStatus, mcpStatus)
	}
	t.Logf("status: paneRef=%s version=%v", mcpStatus["paneRef"], mcpStatus["resourceVersion"])
	ctlCapture := f.call("pane.capture", map[string]any{"paneRef": f.pane["paneRef"], "lines": 10})["result"].(map[string]any)
	mcpCapture := mcpOneCall(t, f, "capture_pane", map[string]any{"paneRef": f.pane["paneRef"], "lines": 10})["result"].(map[string]any)["structuredContent"].(map[string]any)
	if ctlCapture["paneRef"] != mcpCapture["paneRef"] || ctlCapture["text"] != mcpCapture["text"] {
		t.Fatalf("capture parity ctl=%#v mcp=%#v", ctlCapture, mcpCapture)
	}
	t.Logf("capture: bounded-redacted=%v", mcpCapture["redacted"])
	ctlWait := f.call("wait.for_change", map[string]any{"paneRef": f.pane["paneRef"], "afterVersion": -1, "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})["result"].(map[string]any)
	mcpWait := mcpOneCall(t, f, "wait_for_state", map[string]any{"paneRef": f.pane["paneRef"], "afterVersion": -1, "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})["result"].(map[string]any)["structuredContent"].(map[string]any)
	if ctlWait["paneRef"] != mcpWait["paneRef"] {
		t.Fatalf("wait parity ctl=%#v mcp=%#v", ctlWait, mcpWait)
	}
	t.Logf("wait: matched paneRef=%s", mcpWait["paneRef"])
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@agent", "claude")
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@state", "idle")
	if result := mcpOneCall(t, f, "nudge", interactionArgs(f, "parity smoke nudge", "waiting", 2800)); !strings.Contains(fmt.Sprint(result), "effect-delivered-unconfirmed") {
		t.Fatalf("nudge smoke: %#v", result)
	}
	t.Log("nudge: controller admitted typed literal interaction")
}

func mcpCredentialEnv(t *testing.T, credential string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp-credential")
	if err := os.WriteFile(path, []byte(credential), 0600); err != nil {
		t.Fatal(err)
	}
	return append(os.Environ(), "COCKPIT_MCP_CREDENTIAL_FILE="+path)
}

func mcpOneCall(t *testing.T, f *fixture, name string, args map[string]any) map[string]any {
	t.Helper()
	c := exec.Command(f.bin, "mcp-stdio", "--socket", f.socket)
	c.Env = mcpCredentialEnv(t, "test-mcp")
	in, e := c.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = in.Close(); _ = c.Process.Kill(); _ = c.Wait() }()
	s := bufio.NewScanner(out)
	send := func(v any) {
		b, _ := json.Marshal(v)
		if _, e := in.Write(append(b, '\n')); e != nil {
			t.Fatal(e)
		}
	}
	recv := func() map[string]any {
		if !s.Scan() {
			t.Fatal("mcp closed")
		}
		var v map[string]any
		if e := json.Unmarshal(s.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	_ = recv()
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}})
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	return recv()
}
