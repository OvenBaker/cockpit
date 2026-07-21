package core_test

import (
	"bufio"
	"database/sql"
	"encoding/json"
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
}

func TestMCPNudgeRaceUsesControllerCASAndPrivateAudit(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@cockpit_provider", "claude")
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.targetPane, "@cockpit_state", "waiting")
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
	if strings.Count(delta, "send-keys") != 1 {
		t.Fatalf("race delivered duplicate terminal input: %q", delta)
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

func mcpOneCall(t *testing.T, f *fixture, name string, args map[string]any) map[string]any {
	t.Helper()
	c := exec.Command(f.bin, "mcp-stdio", "--socket", f.socket)
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
