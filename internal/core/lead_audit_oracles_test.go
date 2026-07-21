package core_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestEventSubscriptionOwnershipAndConnectionCleanup(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	base := subscriptionCount(t, f)
	owner := openRawSession(t, f.socket, "subscription-owner")
	defer owner.Close()
	writeRaw(t, owner, map[string]any{"jsonrpc": "2.0", "id": "sub", "method": "events.subscribe", "params": map[string]any{}})
	registered := readRaw(t, owner)
	ref := registered["result"].(map[string]any)["subscriptionRef"].(string)
	if subscriptionCount(t, f) != base+1 {
		t.Fatal("owner subscription was not registered")
	}
	foreign := openRawSession(t, f.socket, "subscription-foreign")
	defer foreign.Close()
	writeRaw(t, foreign, map[string]any{"jsonrpc": "2.0", "id": "foreign", "method": "events.unsubscribe", "params": map[string]any{"subscriptionRef": ref}})
	if r := readRaw(t, foreign); !strings.Contains(fmt.Sprint(r), "PERMISSION_DENIED") {
		t.Fatalf("foreign unsubscribe: %#v", r)
	}
	f.refresh()
	if r := f.call("metadata.set_display", f.badgeParams("owner-delivery", ik(0, 901), f.pane["resourceVersion"].(float64))); r["result"] == nil {
		t.Fatalf("owner mutation: %#v", r)
	}
	if r := readRaw(t, owner); r["method"] != "controller.event" {
		t.Fatalf("owner lost subscription after foreign attempt: %#v", r)
	}
	writeRaw(t, owner, map[string]any{"jsonrpc": "2.0", "id": "release", "method": "events.unsubscribe", "params": map[string]any{"subscriptionRef": ref}})
	if r := readRaw(t, owner); r["result"] == nil {
		t.Fatalf("owner unsubscribe: %#v", r)
	}
	if subscriptionCount(t, f) != base {
		t.Fatal("owner unsubscribe leaked subscription")
	}
	f.refresh()
	_ = f.call("metadata.set_display", f.badgeParams("after-release", ik(0, 902), f.pane["resourceVersion"].(float64)))
	_ = owner.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := readRawErr(owner); err == nil {
		t.Fatal("released owner received a later event")
	}
	_ = owner.Close()

	// A connection close removes subscriptions it did not explicitly release.
	closing := openSubscription(t, f.socket)
	if subscriptionCount(t, f) != base+1 {
		t.Fatal("close-cleanup subscription missing")
	}
	_ = closing.Close()
	deadline := time.Now().Add(time.Second)
	for subscriptionCount(t, f) != base && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if subscriptionCount(t, f) != base {
		t.Fatal("connection close leaked subscription")
	}
}

func TestOperationFailureWaiterRegisteredBeforeTransition(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	f.refresh()
	baseSeq, baseWatch := eventSeq(t, f), watcherCount(t, f)
	hold, ack := filepath.Join(f.root, "hold-after_prepared_commit"), filepath.Join(f.root, "barriers", "after_prepared_commit")
	if err := os.WriteFile(hold, []byte("hold"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(hold)
	mutation := f.async(f.badgeParams("failure-wait", ik(0, 903), f.pane["resourceVersion"].(float64)))
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
	waiter := openRawSession(t, f.socket, "failure-waiter")
	defer waiter.Close()
	writeRaw(t, waiter, map[string]any{"jsonrpc": "2.0", "id": "failure-wait", "method": "wait.for_change", "params": map[string]any{"operationRef": op, "afterVersion": 0, "deadline": time.Now().Add(time.Minute).Format(time.RFC3339)}})
	waitForWatcherCount(t, f, baseWatch+1)
	if err = os.WriteFile(filepath.Join(f.root, "disable-metadata"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(f.root, "disable-metadata"))
	if err = os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	if out := <-mutation; !bytes.Contains(out, []byte("CAPABILITY_ABSENT")) {
		t.Fatalf("failure transition did not fail: %s", out)
	}
	r := readRaw(t, waiter)
	if !strings.Contains(fmt.Sprint(r), "operation.failed") {
		t.Fatalf("pre-registered waiter did not receive failure: %#v", r)
	}
	if eventSeq(t, f) != baseSeq+1 {
		t.Fatal("failure emitted more than one terminal event")
	}
	waitForWatcherCount(t, f, baseWatch)
	_ = waiter.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := readRawErr(waiter); err == nil {
		t.Fatal("failure waiter received a second terminal delivery")
	}
}

func TestProtocolEnvelopeIDsAndOutstandingBounds(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	c := openRawSession(t, f.socket, "strict-envelope")
	defer c.Close()
	assertCode := func(label string, request any, code float64) {
		t.Helper()
		writeRaw(t, c, request)
		r := readRaw(t, c)
		if r["error"] == nil || r["error"].(map[string]any)["code"] != code {
			t.Fatalf("%s: %#v", label, r)
		}
		if strings.Contains(label, "invalid id") && r["id"] != nil {
			t.Fatalf("%s echoed an invalid id: %#v", label, r)
		}
	}
	assertCode("unknown envelope", map[string]any{"jsonrpc": "2.0", "id": "unknown-env", "method": "controller.health", "params": map[string]any{}, "extra": true}, -32600)
	for _, id := range []any{nil, true, map[string]any{}, []any{}, 1.5, float64(9007199254740992), ""} {
		assertCode("invalid id", map[string]any{"jsonrpc": "2.0", "id": id, "method": "controller.health", "params": map[string]any{}}, -32600)
	}
	for _, id := range []any{nil, true, map[string]any{}, []any{}, 1.5, ""} {
		assertCode("invalid cancel id", map[string]any{"jsonrpc": "2.0", "id": "cancel", "method": "rpc.cancel", "params": map[string]any{"requestId": id}}, -32602)
	}
	assertCode("forbidden health params", map[string]any{"jsonrpc": "2.0", "id": "health-params", "method": "controller.health", "params": map[string]any{"x": true}}, -32602)
	assertCode("empty pane", map[string]any{"jsonrpc": "2.0", "id": "pane-empty", "method": "pane.inspect", "params": map[string]any{"paneRef": ""}}, -32602)
	assertCode("empty operation", map[string]any{"jsonrpc": "2.0", "id": "op-empty", "method": "operation.get", "params": map[string]any{"operationRef": ""}}, -32602)
	assertCode("empty subscription", map[string]any{"jsonrpc": "2.0", "id": "sub-empty", "method": "events.unsubscribe", "params": map[string]any{"subscriptionRef": ""}}, -32602)
	assertCode("empty wait deadline", map[string]any{"jsonrpc": "2.0", "id": "wait-deadline", "method": "wait.for_change", "params": map[string]any{"paneRef": "p", "afterVersion": 0, "deadline": ""}}, -32602)
	f.refresh()
	base := watcherCount(t, f)
	writeRaw(t, c, waitRequest("same", fmt.Sprint(f.pane["paneRef"]), f.pane["resourceVersion"].(float64)))
	waitForWatcherCount(t, f, base+1)
	assertCode("duplicate in-flight", map[string]any{"jsonrpc": "2.0", "id": "same", "method": "wait.for_change", "params": map[string]any{"paneRef": f.pane["paneRef"], "afterVersion": f.pane["resourceVersion"], "deadline": time.Now().Add(time.Minute).Format(time.RFC3339)}}, -32600)
	if watcherCount(t, f) != base+1 {
		t.Fatal("duplicate request cancelled the original wait")
	}
	writeRaw(t, c, map[string]any{"jsonrpc": "2.0", "id": "cancel-same", "method": "rpc.cancel", "params": map[string]any{"requestId": "same"}})
	for i := 0; i < 2; i++ {
		_ = readRaw(t, c)
	}
	waitForWatcherCount(t, f, base)

	bound := openRawSession(t, f.socket, "outstanding-65")
	defer bound.Close()
	for i := 0; i < 65; i++ {
		writeRaw(t, bound, waitRequest(fmt.Sprintf("bound-%d", i), fmt.Sprint(f.pane["paneRef"]), f.pane["resourceVersion"].(float64)))
	}
	waitForWatcherCount(t, f, base+64)
	limited, live := false, 0
	f.call("metadata.set_display", f.badgeParams("release-bound", ik(0, 904), f.pane["resourceVersion"].(float64)))
	for i := 0; i < 65; i++ {
		r := readRaw(t, bound)
		if strings.Contains(fmt.Sprint(r), "CAPABILITY_ABSENT") {
			limited = true
		}
		if r["result"] != nil {
			live++
		}
	}
	if !limited || live != 64 {
		t.Fatalf("65th bound response did not preserve first 64: limited=%t live=%d", limited, live)
	}
	waitForWatcherCount(t, f, base)
}

// TestBadgeRequiredFieldsOneAtATimeNoAdmissionOrEffect avoids a composite
// invalid request masking a missing runtime validation branch.
func TestBadgeRequiredFieldsOneAtATimeNoAdmissionOrEffect(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	f.refresh()
	db, err := sql.Open("sqlite", filepath.Join(f.root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	local := openLocalRawSession(t, f.socket, "required-fields")
	defer local.Close()
	cases := []struct {
		name     string
		change   func(map[string]any)
		standard bool
	}{
		{"protocol", func(p map[string]any) { p["protocol"] = "" }, true},
		{"deadline", func(p map[string]any) { p["deadline"] = "" }, true},
		{"idempotencyKey", func(p map[string]any) { p["idempotencyKey"] = "" }, true},
		{"paneRef", func(p map[string]any) { p["paneRef"] = "" }, true},
		{"zero-expectations", func(p map[string]any) { p["expectations"] = []any{} }, false},
		{"two-expectations", func(p map[string]any) {
			p["expectations"] = append(p["expectations"].([]any), p["expectations"].([]any)[0])
		}, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := f.badgeParams("required-"+tc.name, ik(0, 1100+i), f.pane["resourceVersion"].(float64))
			tc.change(p)
			beforeOps, beforeSeq := operationCount(t, db), eventSeq(t, f)
			beforeTrace := mustRead(t, filepath.Join(f.root, "driver.trace"))
			writeRaw(t, local, map[string]any{"jsonrpc": "2.0", "id": "required-" + tc.name, "method": "metadata.set_display", "params": p})
			r := readRaw(t, local)
			if tc.standard {
				if r["error"] == nil || r["error"].(map[string]any)["code"] != float64(-32602) {
					t.Fatalf("required %s: %#v", tc.name, r)
				}
			} else if !strings.Contains(fmt.Sprint(r), "INVALID_REQUEST") {
				t.Fatalf("expectation cardinality %s: %#v", tc.name, r)
			}
			if operationCount(t, db) != beforeOps || eventSeq(t, f) != beforeSeq || !bytes.Equal(beforeTrace, mustRead(t, filepath.Join(f.root, "driver.trace"))) {
				t.Fatalf("%s admitted, emitted, or effected a mutation", tc.name)
			}
		})
	}
}

func TestSessionOpenStrictTaxonomy(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	valid := map[string]any{"protocol": "1.0", "clientId": "session-contract", "claimedProfile": "read-only", "credential": "test-read"}
	assertSession := func(name string, id any, params any, code float64, text string) {
		t.Helper()
		c, err := net.Dial("unix", f.socket)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		writeRaw(t, c, map[string]any{"jsonrpc": "2.0", "id": id, "method": "session.open", "params": params})
		r := readRaw(t, c)
		if r["error"] == nil || r["error"].(map[string]any)["code"] != code || (text != "" && !strings.Contains(fmt.Sprint(r), text)) {
			t.Fatalf("%s: %#v", name, r)
		}
		if code == -32600 && r["id"] != nil {
			t.Fatalf("%s echoed invalid id: %#v", name, r)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"empty-protocol", func(p map[string]any) { p["protocol"] = "" }},
		{"empty-client", func(p map[string]any) { p["clientId"] = "" }},
		{"omitted-profile", func(p map[string]any) { delete(p, "claimedProfile") }},
		{"invalid-profile", func(p map[string]any) { p["claimedProfile"] = "nope" }},
	} {
		p := map[string]any{}
		for k, v := range valid {
			p[k] = v
		}
		tc.change(p)
		assertSession(tc.name, "open", p, -32602, "")
	}
	p := map[string]any{}
	for k, v := range valid {
		p[k] = v
	}
	p["protocol"] = "9.0"
	assertSession("unsupported-protocol", "open", p, -32001, "UNSUPPORTED_PROTOCOL")
	p = map[string]any{}
	for k, v := range valid {
		p[k] = v
	}
	p["credential"] = "bad"
	assertSession("bad-credential", "open", p, -32001, "UNAUTHENTICATED")
	assertSession("invalid-preauth-id", true, valid, -32600, "")
	for _, raw := range [][]byte{{0xff}, []byte(`{"jsonrpc":"2.0"`)} {
		c, err := net.Dial("unix", f.socket)
		if err != nil {
			t.Fatal(err)
		}
		writeRawBytes(t, c, raw)
		r := readRaw(t, c)
		_ = c.Close()
		if r["error"].(map[string]any)["code"] != float64(-32700) || r["id"] != nil {
			t.Fatalf("preauth parse taxonomy: %#v", r)
		}
	}
}

func TestPostEffectAmbiguityFencesAndWakesExactlyOnce(t *testing.T) {
	for _, mode := range []string{"driver-ambiguous", "readback-ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			defer f.close()
			f.refresh()
			baseSeq, baseTrace := eventSeq(t, f), mustRead(t, filepath.Join(f.root, "driver.trace"))
			hold, ack := filepath.Join(f.root, "hold-after_prepared_commit"), filepath.Join(f.root, "barriers", "after_prepared_commit")
			if err := os.WriteFile(hold, []byte("hold"), 0600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(hold)
			out := f.async(f.badgeParams("ambiguous-"+mode, ik(0, 905), f.pane["resourceVersion"].(float64)))
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
			waiter := openRawSession(t, f.socket, "ambiguity-waiter")
			defer waiter.Close()
			writeRaw(t, waiter, map[string]any{"jsonrpc": "2.0", "id": "ambiguity-wait", "method": "wait.for_change", "params": map[string]any{"operationRef": op, "afterVersion": 0, "deadline": time.Now().Add(time.Minute).Format(time.RFC3339)}})
			waitForWatcherCount(t, f, 1)
			if err = os.WriteFile(filepath.Join(f.root, mode), []byte("1"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(hold); err != nil {
				t.Fatal(err)
			}
			if got := <-out; !bytes.Contains(got, []byte("INTERNAL")) {
				t.Fatalf("ambiguity mutation did not fail: %s", got)
			}
			if got := readRaw(t, waiter); !strings.Contains(fmt.Sprint(got), "operation.recovery-required") {
				t.Fatalf("ambiguity waiter: %#v", got)
			}
			if eventSeq(t, f) != baseSeq+1 {
				t.Fatal("ambiguity emitted more than one terminal event")
			}
			f.refresh()
			if f.pane["lifecycle"] != "recovery-required" || hasCapability(f.pane, "metadata:write") {
				t.Fatalf("ambiguity did not fence pane: %#v", f.pane)
			}
			if got := f.call("operation.get", map[string]any{"operationRef": op}); got["result"].(map[string]any)["status"] != "recovery-required" {
				t.Fatalf("operation not fenced: %#v", got)
			}
			beforeRetry := mustRead(t, filepath.Join(f.root, "driver.trace"))
			r := f.call("metadata.set_display", f.badgeParams("blocked-after-"+mode, ik(0, 906), f.pane["resourceVersion"].(float64)))
			if !strings.Contains(fmt.Sprint(r), "CONTROLLER_NOT_READY") {
				t.Fatalf("fenced pane admitted another effect: %#v", r)
			}
			if !bytes.Equal(beforeRetry, mustRead(t, filepath.Join(f.root, "driver.trace"))) {
				t.Fatal("fenced pane issued a subsequent effect")
			}
			if mode == "driver-ambiguous" && strings.Contains(string(mustRead(t, filepath.Join(f.root, "driver.trace"))[len(baseTrace):]), "set-option -p") {
				t.Fatal("driver ambiguity hook issued an atomic effect")
			}
			_ = waiter.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			if _, err := readRawErr(waiter); err == nil {
				t.Fatal("ambiguity waiter received a second event")
			}
		})
	}
}

func hasCapability(p map[string]any, want string) bool {
	for _, item := range p["capabilities"].([]any) {
		if item == want {
			return true
		}
	}
	return false
}

func subscriptionCount(t *testing.T, f *fixture) int {
	t.Helper()
	return int(f.call("controller.health", map[string]any{})["result"].(map[string]any)["subscriptionCount"].(float64))
}

func openLocalRawSession(t *testing.T, socket, clientID string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, c, map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": clientID, "claimedProfile": "local-operator", "credential": "test-local"}})
	if r := readRaw(t, c); r["result"] == nil {
		t.Fatalf("local session.open: %#v", r)
	}
	return c
}

// Keep net imported in this file: it documents that these are wire-level
// assertions, not direct daemon method calls.
var _ net.Conn
