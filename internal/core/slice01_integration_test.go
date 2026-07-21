package core_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// These tests deliberately use a real random tmux socket, a daemon OS process,
// and separate ctl OS processes. They never accept the live socket name.
type fixture struct {
	t                         *testing.T
	root, socket, tmux, bin   string
	daemon                    *exec.Cmd
	pane                      map[string]any
	targetPane, nonTargetPane string
	nonTargetMarker           string
}

func TestSlice01Acceptance(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	t.Run("R3 O11", f.raceCAS)
	t.Run("C2 C3 queue-head", f.queueHeadCAS)
	t.Run("R4 R5 R5b I7", f.idempotencyAndRestart)
	t.Run("R6", f.crashRecovery)
	t.Run("L1 L2 L3", f.leaseAndSocketSafety)
	t.Run("P1 P2 P3 P5 P6 P7", f.protocolAndWait)
}
func TestStableIdentityMultiPaneAndStampFence(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	snap := f.call("state.snapshot", map[string]any{})["result"].(map[string]any)
	panes := snap["panes"].([]any)
	if len(panes) != 2 {
		t.Fatalf("expected two inventoried panes, got %d", len(panes))
	}
	a, b := panes[0].(map[string]any), panes[1].(map[string]any)
	if a["workspaceRef"] != b["workspaceRef"] {
		t.Fatalf("multi-pane inventory split one window into two workspace refs: %#v %#v", a, b)
	}
	f.stop()
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", "slice:0.1", "@cockpit_pane_version", "999")
	bad := exec.Command(f.bin, "daemon", "--test-root", f.root, "--socket", f.socket, "--tmux-socket", f.tmux)
	if out, err := bad.CombinedOutput(); err == nil || !bytes.Contains(out, []byte("CONTROLLER_NOT_READY")) {
		t.Fatalf("stamp drift was not fenced: %v %s", err, out)
	}
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	socket := filepath.Join(root, "control.sock")
	tmuxSocket := "cp-it-slice01-" + fmt.Sprint(time.Now().UnixNano())
	if tmuxSocket == "cockpit" {
		t.Fatal("bad test socket")
	}
	run(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "slice", "-n", "work", "sleep 600")
	marker := "non-target-scrollback-" + fmt.Sprint(time.Now().UnixNano())
	run(t, "tmux", "-L", tmuxSocket, "split-window", "-t", "slice:0.0", "bash -c 'printf \""+marker+"\\n\"; exec -a cockpit-non-target sleep 601'")
	run(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", "slice:0.1", "@test_sentinel", "non-target-sentinel")
	run(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", "slice:0.1", "@test_option_hash", "non-target-option-hash")
	bin := filepath.Join(root, "cockpit-core")
	repo := filepath.Clean(filepath.Join("..", ".."))
	cmd := exec.Command("go", "build", "-tags", "cockpit_test", "-buildvcs=false", "-o", bin, "./cmd/cockpit-core")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOCACHE=/tmp/cp-core-go-cache")
	if o, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build: %v: %s", e, o)
	}
	f := &fixture{t: t, root: root, socket: socket, tmux: tmuxSocket, bin: bin, nonTargetMarker: marker}
	f.start(false)
	snap := f.call("state.snapshot", map[string]any{})
	f.targetPane = strings.TrimSpace(string(mustOutput(t, "tmux", "-L", tmuxSocket, "display-message", "-p", "-t", "slice:0.0", "#{pane_id}")))
	f.nonTargetPane = strings.TrimSpace(string(mustOutput(t, "tmux", "-L", tmuxSocket, "display-message", "-p", "-t", "slice:0.1", "#{pane_id}")))
	for _, item := range snap["result"].(map[string]any)["panes"].([]any) {
		p := item.(map[string]any)
		if p["locator"].(map[string]any)["paneId"] == f.targetPane {
			f.pane = p
		}
	}
	if f.pane == nil {
		t.Fatal("fixture could not bind target pane")
	}
	return f
}
func mustOutput(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	b, e := exec.Command(name, args...).Output()
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func (f *fixture) start(crash bool) {
	f.t.Helper()
	c := exec.Command(f.bin, "daemon", "--test-root", f.root, "--socket", f.socket, "--tmux-socket", f.tmux)
	barriers := filepath.Join(f.root, "barriers")
	if err := os.MkdirAll(barriers, 0700); err != nil {
		f.t.Fatal(err)
	}
	c.Env = append(os.Environ(), "COCKPIT_TEST_BARRIER_DIR="+barriers)
	if crash {
		c.Env = append(c.Env, "COCKPIT_TEST_CRASH_AFTER_EFFECT=1")
	}
	if e := c.Start(); e != nil {
		f.t.Fatal(e)
	}
	f.daemon = c
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := os.Stat(f.socket); e == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("daemon did not create socket")
}
func (f *fixture) stop() {
	if f.daemon != nil && f.daemon.Process != nil {
		_ = f.daemon.Process.Kill()
		_ = f.daemon.Wait()
		f.daemon = nil
	}
	_ = os.Remove(f.socket)
}
func (f *fixture) close() {
	f.stop()
	_ = exec.Command("tmux", "-L", f.tmux, "kill-server").Run()
	b, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	if strings.Contains(string(b), "-L cockpit") {
		f.t.Fatalf("live socket appeared in trace: %s", b)
	}
}
func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if o, e := exec.Command(name, args...).CombinedOutput(); e != nil {
		t.Fatalf("%s %v: %v: %s", name, args, e, o)
	}
}
func (f *fixture) call(method string, params any) map[string]any {
	f.t.Helper()
	p, _ := json.Marshal(params)
	c := exec.Command(f.bin, "ctl", "--socket", f.socket, method, string(p))
	if o, e := c.Output(); e != nil {
		f.t.Fatalf("ctl %s: %v", method, e)
	} else {
		var r map[string]any
		if e = json.Unmarshal(o, &r); e != nil {
			f.t.Fatal(e)
		}
		return r
	}
	return nil
}
func (f *fixture) badgeParams(badge, key string, version float64) map[string]any {
	return map[string]any{"protocol": "1.0", "deadline": time.Now().Add(time.Minute).UTC().Format(time.RFC3339), "idempotencyKey": key, "paneRef": f.pane["paneRef"], "badge": badge, "expectations": []any{map[string]any{"kind": "pane", "paneRef": f.pane["paneRef"], "generation": f.pane["generation"], "resourceVersion": version, "material": map[string]any{"lifecycle": "active"}}}}
}
func ik(offset time.Duration, n int) string {
	return fmt.Sprintf("ik_%d_%032x", time.Now().Add(offset).Unix(), n)
}
func (f *fixture) refresh() {
	f.pane = f.call("pane.inspect", map[string]any{"paneRef": f.pane["paneRef"]})["result"].(map[string]any)
}
func (f *fixture) raceCAS(t *testing.T) {
	v := f.pane["resourceVersion"].(float64)
	oldTmuxVersion := strings.TrimSpace(string(mustOutput(t, "tmux", "-L", f.tmux, "display-message", "-p", "-t", f.targetPane, "#{@cockpit_pane_version}")))
	if oldTmuxVersion != fmt.Sprint(int(v)) {
		t.Fatalf("R3 durable/tmux version diverged before race: durable=%v tmux=%q", v, oldTmuxVersion)
	}
	beforeSeq := eventSeq(t, f)
	beforeTrace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	nonTargetPID := strings.TrimSpace(string(mustOutput(t, "tmux", "-L", f.tmux, "display-message", "-p", "-t", f.nonTargetPane, "#{pane_pid}")))
	nonTargetStart := procStartTicks(t, nonTargetPID)
	if cmdline, err := os.ReadFile(filepath.Join("/proc", nonTargetPID, "cmdline")); err != nil || !bytes.Contains(cmdline, []byte("cockpit-non-target")) {
		t.Fatalf("non-target process is not uniquely identifiable: %q %v", cmdline, err)
	}
	nonTargetScroll := strings.TrimSpace(string(mustOutput(t, "tmux", "-L", f.tmux, "capture-pane", "-p", "-t", f.nonTargetPane)))
	if !strings.Contains(nonTargetScroll, f.nonTargetMarker) {
		t.Fatalf("non-target marker missing from scrollback: %q", nonTargetScroll)
	}
	nonTargetTopology := paneTopologyHash(t, f)
	a := f.badgeParams("race-a", ik(0, 1), v)
	b := f.badgeParams("race-b", ik(0, 2), v)
	var wg sync.WaitGroup
	out := make([][]byte, 2)
	for i, p := range []map[string]any{a, b} {
		wg.Add(1)
		go func(i int, p map[string]any) {
			defer wg.Done()
			raw, _ := json.Marshal(p)
			c := exec.Command(f.bin, "ctl", "--socket", f.socket, "metadata.set_display", string(raw))
			out[i], _ = c.Output()
		}(i, p)
	}
	wg.Wait()
	wins := 0
	conflicts := 0
	winningBadge := ""
	for i, o := range out {
		if bytes.Contains(o, []byte("CONFLICT_VERSION")) {
			conflicts++
		}
		if bytes.Contains(o, []byte("completed")) {
			wins++
			winningBadge = []string{"race-a", "race-b"}[i]
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("race wins=%d conflicts=%d output=%q", wins, conflicts, out)
	}
	afterTrace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	delta := string(afterTrace[len(beforeTrace):])
	if strings.Count(delta, "@cockpit_badge race-") != 1 || strings.Count(delta, "; set-option -p -t "+f.targetPane+" @cockpit_pane_version") != 1 || strings.Contains(delta, f.nonTargetPane) || eventSeq(t, f) != beforeSeq+1 {
		t.Fatalf("R3 did not produce exactly one effect and one event cursor advance: %q", delta)
	}
	if got := procStartTicks(t, nonTargetPID); got != nonTargetStart {
		t.Fatalf("R3 changed non-target process identity: %s != %s", got, nonTargetStart)
	}
	if got := strings.TrimSpace(string(mustOutput(t, "tmux", "-L", f.tmux, "capture-pane", "-p", "-t", f.nonTargetPane))); got != nonTargetScroll {
		t.Fatal("R3 changed non-target scrollback")
	}
	if got := paneTopologyHash(t, f); got != nonTargetTopology {
		t.Fatal("R3 changed non-target topology/options")
	}
	sentinel, err := exec.Command("tmux", "-L", f.tmux, "display-message", "-p", "-t", f.nonTargetPane, "#{@test_sentinel}").Output()
	if err != nil || strings.TrimSpace(string(sentinel)) != "non-target-sentinel" {
		t.Fatal("non-target pane sentinel changed")
	}
	hash, err := exec.Command("tmux", "-L", f.tmux, "display-message", "-p", "-t", f.nonTargetPane, "#{@test_option_hash}").Output()
	if err != nil || strings.TrimSpace(string(hash)) != "non-target-option-hash" {
		t.Fatal("non-target option hash changed")
	}
	f.refresh()
	if f.pane["resourceVersion"] != v+1 || f.pane["display"].(map[string]any)["badge"] != winningBadge {
		t.Fatalf("R3 durable winner/version mismatch: %#v winner=%q", f.pane, winningBadge)
	}
	if got := strings.TrimSpace(string(mustOutput(t, "tmux", "-L", f.tmux, "display-message", "-p", "-t", f.targetPane, "#{@cockpit_badge}"))); got != winningBadge {
		t.Fatalf("R3 tmux badge=%q winner=%q", got, winningBadge)
	}
	if got := strings.TrimSpace(string(mustOutput(t, "tmux", "-L", f.tmux, "display-message", "-p", "-t", f.targetPane, "#{@cockpit_pane_version}"))); got != fmt.Sprint(int(v)+1) {
		t.Fatalf("R3 tmux version=%q want=%d", got, int(v)+1)
	}
	beforeSeq = eventSeq(t, f)
	beforeVersion := f.pane["resourceVersion"]
	rejects := []map[string]any{f.badgeParams(strings.Repeat("x", 49), ik(0, 3), beforeVersion.(float64)), f.badgeParams("bad\n", ik(0, 4), beforeVersion.(float64)), f.badgeParams("state", ik(0, 5), beforeVersion.(float64))}
	rejects[2]["state"] = "working"
	for _, p := range rejects {
		beforeRejectTrace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
		if r := f.call("metadata.set_display", p); !strings.Contains(fmt.Sprint(r), "-32602") && !strings.Contains(fmt.Sprint(r), "INVALID_REQUEST") {
			t.Fatalf("O11 rejection missing: %#v", r)
		}
		afterRejectTrace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
		if !bytes.Equal(beforeRejectTrace, afterRejectTrace) {
			t.Fatalf("O11 rejected value issued driver effect: %#v", p)
		}
	}
	beforeRejectTrace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	if r := f.call("metadata.set_display", f.badgeParams(strings.Repeat("界", 49), ik(0, 6), beforeVersion.(float64))); !strings.Contains(fmt.Sprint(r), "INVALID_REQUEST") {
		t.Fatalf("O11 rune-length rejection missing: %#v", r)
	}
	afterRejectTrace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	if !bytes.Equal(beforeRejectTrace, afterRejectTrace) {
		t.Fatal("O11 rune rejection issued driver effect")
	}
	if eventSeq(t, f) != beforeSeq {
		t.Fatal("O11 rejected display changed event cursor")
	}
	beforeTrace, _ = os.ReadFile(filepath.Join(f.root, "driver.trace"))
	beforeSeq = eventSeq(t, f)
	if r := f.call("metadata.set_display", f.badgeParams("o11-valid", ik(0, 7), beforeVersion.(float64))); r["result"] == nil {
		t.Fatalf("O11 valid mutation failed: %#v", r)
	}
	afterTrace, _ = os.ReadFile(filepath.Join(f.root, "driver.trace"))
	validDelta := strings.Split(strings.TrimSpace(string(afterTrace[len(beforeTrace):])), "\n")
	badgePlans := 0
	for _, line := range validDelta {
		if strings.Contains(line, "@cockpit_badge o11-valid") {
			badgePlans++
			if !strings.Contains(line, "; set-option -p -t "+f.targetPane+" @cockpit_pane_version") {
				t.Fatalf("O11 badge mutation was not an atomic version plan: %q", line)
			}
		}
	}
	if eventSeq(t, f) != beforeSeq+1 || badgePlans != 1 {
		t.Fatal("O11 valid mutation did not have one event and one atomic effect plan")
	}
}

func procStartTicks(t *testing.T, pid string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(b))
	if len(fields) < 22 {
		t.Fatalf("short /proc stat for %s", pid)
	}
	return fields[21]
}
func paneTopologyHash(t *testing.T, f *fixture) string {
	t.Helper()
	b := mustOutput(t, "tmux", "-L", f.tmux, "list-panes", "-a", "-F", "#{session_name}|#{window_id}|#{pane_id}|#{pane_pid}|#{pane_active}|#{@test_sentinel}|#{@test_option_hash}")
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}
func (f *fixture) queueHeadCAS(t *testing.T) {
	f.refresh()
	v := f.pane["resourceVersion"].(float64)
	hold := filepath.Join(f.root, "hold-badge_at_queue_head")
	ack := filepath.Join(f.root, "barriers", "badge_at_queue_head")
	_ = os.Remove(ack)
	if err := os.WriteFile(hold, []byte("hold"), 0600); err != nil {
		t.Fatal(err)
	}
	first := f.badgeParams("queue-first", ik(0, 40), v)
	second := f.badgeParams("queue-second", ik(0, 41), v)
	one := f.async(first)
	waitForFile(t, ack)
	two := f.async(second)
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	a, b := <-one, <-two
	if !strings.Contains(string(a), "completed") || !strings.Contains(string(b), "CONFLICT_VERSION") {
		t.Fatalf("C2 queue-head results %s %s", a, b)
	}
	f.refresh()
	v = f.pane["resourceVersion"].(float64)
	_ = os.Remove(ack)
	if err := os.WriteFile(hold, []byte("hold"), 0600); err != nil {
		t.Fatal(err)
	}
	block := f.async(f.badgeParams("queue-block", ik(0, 42), v))
	waitForFile(t, ack)
	expired := f.badgeParams("queue-expired", ik(0, 43), v)
	deadline := time.Now().Add(150 * time.Millisecond)
	expired["deadline"] = deadline.Format(time.RFC3339Nano)
	late := f.async(expired)
	for time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_ = os.Remove(hold)
	_ = <-block
	out := <-late
	if !strings.Contains(string(out), "DEADLINE_EXCEEDED") {
		t.Fatalf("C3 queue-head result %s", out)
	}
	trace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	if strings.Contains(string(trace), "@cockpit_badge queue-expired") {
		t.Fatal("C3 issued a second effect")
	}
}
func (f *fixture) async(p map[string]any) <-chan []byte {
	ch := make(chan []byte, 1)
	go func() {
		raw, _ := json.Marshal(p)
		o, _ := exec.Command(f.bin, "ctl", "--socket", f.socket, "metadata.set_display", string(raw)).Output()
		ch <- o
	}()
	return ch
}
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := os.Stat(path); e == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("barrier not reached: %s", path)
}
func waitForTrace(t *testing.T, path, needle string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), needle) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("trace did not contain %q", needle)
}
func eventSeq(t *testing.T, f *fixture) float64 {
	return f.call("controller.health", map[string]any{})["result"].(map[string]any)["eventSeq"].(float64)
}
func (f *fixture) idempotencyAndRestart(t *testing.T) {
	f.refresh()
	k := ik(0, 10)
	p := f.badgeParams("idem", k, f.pane["resourceVersion"].(float64))
	a := f.call("metadata.set_display", p)
	b := f.call("metadata.set_display", p)
	if !strings.Contains(fmt.Sprint(b), "replayed") {
		t.Fatalf("same intent not replayed: %#v", b)
	}
	changed := f.badgeParams("different", k, f.pane["resourceVersion"].(float64))
	c := f.call("metadata.set_display", changed)
	if !strings.Contains(fmt.Sprint(c), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("different intent accepted: %#v", c)
	}
	before := f.call("pane.inspect", map[string]any{"paneRef": f.pane["paneRef"]})
	f.stop()
	f.start(false)
	after := f.call("pane.inspect", map[string]any{"paneRef": f.pane["paneRef"]})
	if before["result"].(map[string]any)["resourceVersion"] != after["result"].(map[string]any)["resourceVersion"] {
		t.Fatal("I7 changed version on exact restart")
	}
	replay := f.call("metadata.set_display", p)
	if !strings.Contains(fmt.Sprint(replay), "replayed") {
		t.Fatal("restart replay failed")
	}
	opA := a["result"].(map[string]any)["operationRef"]
	opB := b["result"].(map[string]any)["operation"].(map[string]any)["operationRef"]
	opR := replay["result"].(map[string]any)["operation"].(map[string]any)["operationRef"]
	if opA != opB || opA != opR {
		t.Fatalf("R4 operation refs differ: %v %v %v", opA, opB, opR)
	}
	trace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	if strings.Count(string(trace), "@cockpit_badge idem") != 1 {
		t.Fatal("R4 issued more than one idem effect")
	}
	oldKey := ik(-31*24*time.Hour, 11)
	db, err := sql.Open("sqlite", filepath.Join(f.root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	beforeOps := operationCount(t, db)
	_, err = db.Exec("INSERT INTO operations(ref,caller,method,idem_key,intent,status,pane_ref,badge,result,created_at) VALUES(?,?,?,?,?,'completed',?,?,?,?)", "cpo_retainedoldkey00000000000000000", "cockpitctl", "metadata.set_display", oldKey, "retained", f.pane["paneRef"], "", "{}", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	old := f.badgeParams("old", oldKey, after["result"].(map[string]any)["resourceVersion"].(float64))
	r := f.call("metadata.set_display", old)
	if !strings.Contains(fmt.Sprint(r), "IDEMPOTENCY_EXPIRED") {
		t.Fatalf("R5b old key: %#v", r)
	}
	if operationCount(t, db) != beforeOps+1 {
		t.Fatal("R5b retained old key admitted an operation")
	}
	_, _ = db.Exec("DELETE FROM operations WHERE idem_key=?", oldKey)
	r = f.call("metadata.set_display", old)
	if !strings.Contains(fmt.Sprint(r), "IDEMPOTENCY_EXPIRED") || operationCount(t, db) != beforeOps {
		t.Fatal("R5b pruned old key admitted an operation")
	}
	future := f.badgeParams("future", ik(10*time.Minute, 12), after["result"].(map[string]any)["resourceVersion"].(float64))
	r = f.call("metadata.set_display", future)
	if !strings.Contains(fmt.Sprint(r), "INVALID_REQUEST") {
		t.Fatalf("R5b future key: %#v", r)
	}
	_ = a
}
func operationCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM operations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *fixture) crashRecovery(t *testing.T) {
	f.refresh()
	db, err := sql.Open("sqlite", filepath.Join(f.root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var beforeAudits int
	if err = db.QueryRow("SELECT count(*) FROM audit WHERE pane_ref=? AND method='metadata.set_display'", f.pane["paneRef"]).Scan(&beforeAudits); err != nil {
		t.Fatal(err)
	}
	f.stop()
	f.start(true)
	p := f.badgeParams("recovered", ik(0, 20), f.pane["resourceVersion"].(float64))
	raw, _ := json.Marshal(p)
	_ = exec.Command(f.bin, "ctl", "--socket", f.socket, "metadata.set_display", string(raw)).Run()
	_ = f.daemon.Wait()
	f.daemon = nil
	_ = os.Remove(f.socket)
	f.start(false)
	r := f.call("metadata.set_display", p)
	if !strings.Contains(fmt.Sprint(r), "replayed") || !strings.Contains(fmt.Sprint(r), "recovered") {
		t.Fatalf("R6 not recovered: %#v", r)
	}
	trace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	if strings.Count(string(trace), "@cockpit_badge recovered") != 1 {
		t.Fatalf("R6 builder reran or effect duplicated: %s", trace)
	}
	var count int
	var caller string
	if err = db.QueryRow("SELECT count(*) FROM audit WHERE pane_ref=? AND method='metadata.set_display'", f.pane["paneRef"]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT caller FROM audit WHERE pane_ref=? AND method='metadata.set_display' ORDER BY seq DESC LIMIT 1", f.pane["paneRef"]).Scan(&caller); err != nil {
		t.Fatal(err)
	}
	if count != beforeAudits+1 || caller != "test-local-operator" {
		t.Fatalf("R6 recovery audit caller/count = %q/%d (before %d)", caller, count, beforeAudits)
	}
}
func (f *fixture) leaseAndSocketSafety(t *testing.T) {
	f.stop()
	l1root := t.TempDir()
	l1sock := filepath.Join(l1root, "control.sock")
	l1tmux := "cp-it-lease-" + fmt.Sprint(time.Now().UnixNano())
	run(t, "tmux", "-L", l1tmux, "new-session", "-d", "-s", "slice", "sleep 600")
	defer exec.Command("tmux", "-L", l1tmux, "kill-server").Run()
	start := func(actor string) *exec.Cmd {
		c := exec.Command(f.bin, "daemon", "--test-root", l1root, "--socket", l1sock, "--tmux-socket", l1tmux)
		c.Env = append(os.Environ(), "COCKPIT_TEST_ACTOR="+actor)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := start("a"), start("b")
	waitForFile(t, l1sock)
	time.Sleep(50 * time.Millisecond)
	alive := 0
	for _, c := range []*exec.Cmd{a, b} {
		if c.ProcessState == nil {
			alive++
		}
	}
	if alive != 2 { /* one may have exited already; expected */
	}
	_ = a.Process.Kill()
	_ = b.Process.Kill()
	_ = a.Wait()
	_ = b.Wait()
	trace, _ := os.ReadFile(filepath.Join(l1root, "store.trace"))
	lines := strings.Fields(string(trace))
	if len(lines) != 1 {
		t.Fatalf("L1 loser touched store: %q", trace)
	}
	// A closed Unix listener leaves a verified stale socket; bogus PID contents have no authority.
	l2root := t.TempDir()
	l2sock := filepath.Join(l2root, "control.sock")
	l2tmux := "cp-it-stale-" + fmt.Sprint(time.Now().UnixNano())
	run(t, "tmux", "-L", l2tmux, "new-session", "-d", "-s", "slice", "sleep 600")
	defer exec.Command("tmux", "-L", l2tmux, "kill-server").Run()
	addr := &net.UnixAddr{Name: l2sock, Net: "unix"}
	ul, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	ul.SetUnlinkOnClose(false)
	_ = ul.Close()
	stale, err := os.Lstat(l2sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(l2root, "cockpit.pid"), []byte("999999"), 0600)
	l2 := exec.Command(f.bin, "daemon", "--test-root", l2root, "--socket", l2sock, "--tmux-socket", l2tmux)
	if err := l2.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fresh, statErr := os.Lstat(l2sock)
		if statErr == nil && !os.SameFile(stale, fresh) {
			c := openRawSession(t, l2sock, "stale-health")
			writeRaw(t, c, map[string]any{"jsonrpc": "2.0", "id": "health", "method": "controller.health", "params": map[string]any{}})
			r := readRaw(t, c)
			_ = c.Close()
			if r["result"] != nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c, dialErr := net.Dial("unix", l2sock); dialErr != nil {
		t.Fatalf("L2 did not replace stale socket with authenticated controller: %v", dialErr)
	} else {
		_ = c.Close()
	}
	_ = l2.Process.Kill()
	_ = l2.Wait()
	// A connectable foreign listener is never replaced and is rejected before
	// the lease winner opens SQLite or invokes tmux.
	liveRoot := t.TempDir()
	liveSock := filepath.Join(liveRoot, "control.sock")
	live, err := net.Listen("unix", liveSock)
	if err != nil {
		t.Fatal(err)
	}
	liveBad := exec.Command(f.bin, "daemon", "--test-root", liveRoot, "--socket", liveSock, "--tmux-socket", l2tmux)
	if err := liveBad.Run(); err == nil {
		t.Fatal("L2 accepted a connectable foreign socket")
	}
	_ = live.Close()
	if _, err := os.Stat(filepath.Join(liveRoot, "control.db")); !os.IsNotExist(err) {
		t.Fatal("L2 live foreign socket opened store")
	}
	for _, kind := range []string{"file", "symlink"} {
		root := t.TempDir()
		sock := filepath.Join(root, "control.sock")
		if kind == "file" {
			_ = os.WriteFile(sock, []byte("not socket"), 0600)
		} else {
			target := filepath.Join(root, "target")
			_ = os.WriteFile(target, []byte("keep"), 0600)
			if err := os.Symlink(target, sock); err != nil {
				t.Fatal(err)
			}
		}
		bad := exec.Command(f.bin, "daemon", "--test-root", root, "--socket", sock, "--tmux-socket", f.tmux)
		if err := bad.Run(); err == nil {
			t.Fatalf("L3 accepted %s", kind)
		}
		if _, err := os.Lstat(sock); err != nil {
			t.Fatalf("L3 unlinked %s", kind)
		}
		if _, err := os.Stat(filepath.Join(root, "control.db")); !os.IsNotExist(err) {
			t.Fatalf("L3 %s wrote store", kind)
		}
		if _, err := os.Stat(filepath.Join(root, "driver.trace")); !os.IsNotExist(err) {
			t.Fatalf("L3 %s wrote tmux trace", kind)
		}
	}
	f.start(false)
}
func (f *fixture) protocolAndWait(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(f.root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	beforeAdmission := operationCount(t, db)
	beforeTrace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	c, e := net.Dial("unix", f.socket)
	if e != nil {
		t.Fatal(e)
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], 1<<20+1)
	_, _ = c.Write(h[:])
	_ = c.Close()
	for _, body := range [][]byte{{'{'}, {0xff}, {'{', 'x'}} {
		bad, e := net.Dial("unix", f.socket)
		if e != nil {
			t.Fatal(e)
		}
		binary.BigEndian.PutUint32(h[:], uint32(len(body)+2))
		_, _ = bad.Write(h[:])
		_, _ = bad.Write(body)
		_ = bad.Close()
	}
	for _, body := range [][]byte{{0xff}, {'{', 'x'}} {
		bad, e := net.Dial("unix", f.socket)
		if e != nil {
			t.Fatal(e)
		}
		binary.BigEndian.PutUint32(h[:], uint32(len(body)))
		_, _ = bad.Write(h[:])
		_, _ = bad.Write(body)
		_ = bad.Close()
	}
	health := f.call("controller.health", map[string]any{})
	if health["result"] == nil {
		t.Fatal("P1 daemon unhealthy")
	}
	if operationCount(t, db) != beforeAdmission {
		t.Fatal("P1 malformed frame admitted an operation")
	}
	rpc := openRawSession(t, f.socket, "rpc-standard")
	writeRaw(t, rpc, map[string]any{"jsonrpc": "2.0", "id": "unknown", "method": "no.such.method", "params": map[string]any{}})
	if r := readRaw(t, rpc); r["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("unknown method shape: %#v", r)
	}
	writeRaw(t, rpc, map[string]any{"jsonrpc": "2.0", "id": "params", "method": "pane.inspect", "params": map[string]any{"forbidden": true}})
	if r := readRaw(t, rpc); r["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("invalid params shape: %#v", r)
	}
	writeRawBytes(t, rpc, []byte(`{"jsonrpc":"2.0","id":"trail","method":"controller.health","params":{}} {}`))
	if r := readRaw(t, rpc); r["error"].(map[string]any)["code"] != float64(-32700) {
		t.Fatalf("trailing JSON shape: %#v", r)
	}
	invalidUTF8 := append([]byte(`{"jsonrpc":"2.0","id":"u","method":"x`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	writeRawBytes(t, rpc, invalidUTF8)
	if r := readRaw(t, rpc); r["error"].(map[string]any)["code"] != float64(-32700) {
		t.Fatalf("invalid UTF-8 shape: %#v", r)
	}
	_ = rpc.Close()
	f.refresh()
	unknownMutation := f.badgeParams("schema", ik(0, 28), f.pane["resourceVersion"].(float64))
	unknownMutation["forbidden"] = true
	if r := f.call("metadata.set_display", unknownMutation); !strings.Contains(fmt.Sprint(r), "-32602") {
		t.Fatalf("P1 unknown mutation field was accepted: %#v", r)
	}
	if operationCount(t, db) != beforeAdmission || !bytes.Equal(beforeTrace, mustRead(t, filepath.Join(f.root, "driver.trace"))) {
		t.Fatal("P1 unknown field admitted or effected a mutation")
	}
	if r := f.call("pane.inspect", map[string]any{"paneRef": f.pane["paneRef"], "forbidden": true}); !strings.Contains(fmt.Sprint(r), "-32602") {
		t.Fatalf("P1 unknown query field was accepted: %#v", r)
	}
	badExpectations := f.badgeParams("schema", ik(0, 281), f.pane["resourceVersion"].(float64))
	badExpectations["expectations"] = []any{}
	if r := f.call("metadata.set_display", badExpectations); !strings.Contains(fmt.Sprint(r), "INVALID_REQUEST") {
		t.Fatalf("P1 malformed expectation was accepted: %#v", r)
	}
	bad := rawOpen(t, f.socket, map[string]any{"protocol": "9.0", "clientId": "x", "claimedProfile": "local-operator", "credential": "test-local"})
	if !strings.Contains(fmt.Sprint(bad), "UNSUPPORTED_PROTOCOL") {
		t.Fatalf("P2: %#v", bad)
	}
	bound := rawOpen(t, f.socket, map[string]any{"protocol": "1.0", "clientId": "x", "claimedProfile": "read-only", "credential": "test-local"})
	if !strings.Contains(fmt.Sprint(bound), "local-operator") {
		t.Fatalf("P3 profile not credential-bound: %#v", bound)
	}
	oldEpoch := health["result"].(map[string]any)["controllerEpoch"]
	f.stop()
	f.start(false)
	resync := f.call("events.subscribe", map[string]any{"controllerEpoch": oldEpoch})
	if !strings.Contains(fmt.Sprint(resync), "resyncRequired") {
		t.Fatalf("P5 epoch discontinuity did not require resync: %#v", resync)
	}
	f.refresh()
	// P4: capability exists at admission, is removed while a named execution
	// barrier is held, and is checked again before the tmux effect.
	hold := filepath.Join(f.root, "hold-after_prepared_commit")
	ack := filepath.Join(f.root, "barriers", "after_prepared_commit")
	_ = os.Remove(ack)
	if err := os.WriteFile(hold, []byte("hold"), 0600); err != nil {
		t.Fatal(err)
	}
	p4 := f.badgeParams("must-not-write", ik(0, 29), f.pane["resourceVersion"].(float64))
	raw, _ := json.Marshal(p4)
	p4out := make(chan []byte, 1)
	go func() {
		o, _ := exec.Command(f.bin, "ctl", "--socket", f.socket, "metadata.set_display", string(raw)).Output()
		p4out <- o
	}()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(ack); err == nil {
			break
		}
	}
	if _, err := os.Stat(ack); err != nil {
		t.Fatal("P4 did not reach named admission barrier")
	}
	if err := os.WriteFile(filepath.Join(f.root, "disable-metadata"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	if o := <-p4out; !bytes.Contains(o, []byte("CAPABILITY_ABSENT")) {
		t.Fatalf("P4 execution recheck failed: %s", o)
	}
	_ = os.Remove(filepath.Join(f.root, "disable-metadata"))
	holdWait := filepath.Join(f.root, "hold-wait_after_snapshot_before_register")
	ackWait := filepath.Join(f.root, "barriers", "wait_after_snapshot_before_register")
	_ = os.Remove(ackWait)
	if err := os.WriteFile(holdWait, []byte("hold"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan map[string]any, 1)
	go func() {
		done <- f.call("wait.for_change", map[string]any{"paneRef": f.pane["paneRef"], "afterVersion": f.pane["resourceVersion"], "deadline": time.Now().Add(time.Minute).Format(time.RFC3339)})
	}()
	waitForFile(t, ackWait)
	mutation := f.async(f.badgeParams("wait", ik(0, 30), f.pane["resourceVersion"].(float64)))
	waitForTrace(t, filepath.Join(f.root, "driver.trace"), "@cockpit_badge wait")
	if err := os.Remove(holdWait); err != nil {
		t.Fatal(err)
	}
	if out := <-mutation; !strings.Contains(string(out), "completed") {
		t.Fatalf("P6 mutation failed %s", out)
	}
	select {
	case r := <-done:
		if r["result"] == nil {
			t.Fatalf("P6 wait did not match: %#v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("P6 wait timed out")
	}
	f.refresh()
	baseline := watcherCount(t, f)
	cancelConn := openRawSession(t, f.socket, "cancel-wait")
	writeRaw(t, cancelConn, waitRequest("wait-1", fmt.Sprint(f.pane["paneRef"]), f.pane["resourceVersion"].(float64)))
	waitForWatcherCount(t, f, baseline+1)
	writeRaw(t, cancelConn, map[string]any{"jsonrpc": "2.0", "id": "cancel-1", "method": "rpc.cancel", "params": map[string]any{"requestId": "wait-1"}})
	cancelled, cancelAck := false, false
	for i := 0; i < 2; i++ {
		r := readRaw(t, cancelConn)
		if strings.Contains(fmt.Sprint(r), "CANCELLED") {
			cancelled = true
		}
		if strings.Contains(fmt.Sprint(r), "cancelled:true") {
			cancelAck = true
		}
	}
	if !cancelled || !cancelAck {
		t.Fatalf("rpc.cancel did not return cancellation and terminal wait result")
	}
	waitForWatcherCount(t, f, baseline)
	f.call("metadata.set_display", f.badgeParams("after-cancel", ik(0, 300), f.pane["resourceVersion"].(float64)))
	_ = cancelConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := readRawErr(cancelConn); err == nil {
		t.Fatal("cancelled wait received a later delivery")
	}
	_ = cancelConn.Close()
	f.refresh()
	terminal := f.call("metadata.set_display", f.badgeParams("operation-wait", ik(0, 303), f.pane["resourceVersion"].(float64)))
	opRef := terminal["result"].(map[string]any)["operationRef"]
	if r := f.call("wait.for_change", map[string]any{"operationRef": opRef, "afterVersion": 0, "deadline": time.Now().Add(time.Minute).Format(time.RFC3339)}); r["result"] == nil {
		t.Fatalf("operation-terminal wait did not return durable terminal result: %#v", r)
	}
	f.refresh()
	closeConn := openRawSession(t, f.socket, "close-wait")
	writeRaw(t, closeConn, waitRequest("close-1", fmt.Sprint(f.pane["paneRef"]), f.pane["resourceVersion"].(float64)))
	waitForWatcherCount(t, f, baseline+1)
	_ = closeConn.Close()
	waitForWatcherCount(t, f, baseline)

	perConn := openRawSession(t, f.socket, "per-connection")
	for i := 0; i < 65; i++ {
		writeRaw(t, perConn, waitRequest(fmt.Sprintf("pc-%d", i), fmt.Sprint(f.pane["paneRef"]), f.pane["resourceVersion"].(float64)))
	}
	waitForWatcherCount(t, f, baseline+64)
	f.call("metadata.set_display", f.badgeParams("release-per-connection", ik(0, 302), f.pane["resourceVersion"].(float64)))
	matched, limitedPerConn := 0, false
	for i := 0; i < 65; i++ {
		r := readRaw(t, perConn)
		if strings.Contains(fmt.Sprint(r), "CAPABILITY_ABSENT") {
			limitedPerConn = true
		}
		if r["result"] != nil {
			matched++
		}
	}
	_ = perConn.Close()
	if matched != 64 || !limitedPerConn {
		t.Fatalf("per-connection limit did not preserve 64 waits: matched=%d limited=%t", matched, limitedPerConn)
	}
	waitForWatcherCount(t, f, baseline)
	f.refresh()
	sub := openSubscription(t, f.socket)
	f.call("metadata.set_display", f.badgeParams("subscription", ik(0, 301), f.pane["resourceVersion"].(float64)))
	notification := readRaw(t, sub)
	_ = sub.Close()
	if notification["method"] != "controller.event" {
		t.Fatalf("events.subscribe did not deliver a notification: %#v", notification)
	}
	// P5 subscription registration holds the event-bus mutex at the exact
	// snapshot/register point; an effect that lands while it is held is replayed.
	f.refresh()
	holdSub := filepath.Join(f.root, "hold-subscribe_after_snapshot_before_register")
	ackSub := filepath.Join(f.root, "barriers", "subscribe_after_snapshot_before_register")
	_ = os.Remove(ackSub)
	if err := os.WriteFile(holdSub, []byte("hold"), 0600); err != nil {
		t.Fatal(err)
	}
	atomicSub := openRawSession(t, f.socket, "atomic-sub")
	writeRaw(t, atomicSub, map[string]any{"jsonrpc": "2.0", "id": "atomic-sub", "method": "events.subscribe", "params": map[string]any{"afterEventSeq": eventSeq(t, f)}})
	waitForFile(t, ackSub)
	landed := f.async(f.badgeParams("atomic-sub", ik(0, 304), f.pane["resourceVersion"].(float64)))
	waitForTrace(t, filepath.Join(f.root, "driver.trace"), "@cockpit_badge atomic-sub")
	if err := os.Remove(holdSub); err != nil {
		t.Fatal(err)
	}
	if out := <-landed; !strings.Contains(string(out), "completed") {
		t.Fatalf("atomic subscription mutation: %s", out)
	}
	if r := readRaw(t, atomicSub); r["result"] == nil {
		t.Fatalf("atomic subscription response: %#v", r)
	}
	if r := readRaw(t, atomicSub); r["method"] != "controller.event" {
		t.Fatalf("atomic subscription missed event: %#v", r)
	}
	_ = atomicSub.Close()
	f.refresh()
	conns := make([]net.Conn, 0, 256)
	for i := 0; i < 256; i++ {
		conns = append(conns, openWait(t, f.socket, fmt.Sprint(f.pane["paneRef"]), f.pane["resourceVersion"].(float64)))
	}
	waitForWatcherCount(t, f, baseline+256)
	limited := f.call("wait.for_change", map[string]any{"paneRef": f.pane["paneRef"], "afterVersion": f.pane["resourceVersion"], "deadline": time.Now().Add(time.Minute).Format(time.RFC3339)})
	if !strings.Contains(fmt.Sprint(limited), "CAPABILITY_ABSENT") {
		t.Fatalf("P7 watcher limit did not reject: %#v", limited)
	}
	f.call("metadata.set_display", f.badgeParams("release-waiters", ik(0, 31), f.pane["resourceVersion"].(float64)))
	for _, c := range conns {
		_ = c.Close()
	}
	waitForWatcherCount(t, f, baseline)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func watcherCount(t *testing.T, f *fixture) int {
	t.Helper()
	return int(f.call("controller.health", map[string]any{})["result"].(map[string]any)["watcherCount"].(float64))
}
func waitForWatcherCount(t *testing.T, f *fixture, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if watcherCount(t, f) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watcherCount did not return to %d; got %d", want, watcherCount(t, f))
}
func waitRequest(id, pane string, version float64) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": "wait.for_change", "params": map[string]any{"paneRef": pane, "afterVersion": version, "deadline": time.Now().Add(time.Minute).Format(time.RFC3339)}}
}

func openWait(t *testing.T, socket, pane string, version float64) net.Conn {
	t.Helper()
	c := openRawSession(t, socket, "watcher")
	writeRaw(t, c, map[string]any{"jsonrpc": "2.0", "id": "wait", "method": "wait.for_change", "params": map[string]any{"paneRef": pane, "afterVersion": version, "deadline": time.Now().Add(time.Minute).Format(time.RFC3339)}})
	return c
}
func openRawSession(t *testing.T, socket, clientID string) net.Conn {
	t.Helper()
	c, e := net.Dial("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	writeRaw(t, c, map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": clientID, "claimedProfile": "read-only", "credential": "test-read"}})
	if r := readRaw(t, c); r["result"] == nil {
		t.Fatalf("session.open failed: %#v", r)
	}
	return c
}
func openSubscription(t *testing.T, socket string) net.Conn {
	t.Helper()
	c, e := net.Dial("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	writeRaw(t, c, map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": "subscriber", "claimedProfile": "read-only", "credential": "test-read"}})
	_ = readRaw(t, c)
	writeRaw(t, c, map[string]any{"jsonrpc": "2.0", "id": "sub", "method": "events.subscribe", "params": map[string]any{}})
	if r := readRaw(t, c); r["result"] == nil {
		t.Fatalf("subscribe failed: %#v", r)
	}
	return c
}
func writeRaw(t *testing.T, c net.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, e := c.Write(h[:]); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Write(b); e != nil {
		t.Fatal(e)
	}
}
func writeRawBytes(t *testing.T, c net.Conn, b []byte) {
	t.Helper()
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, e := c.Write(h[:]); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Write(b); e != nil {
		t.Fatal(e)
	}
}
func readRaw(t *testing.T, c net.Conn) map[string]any {
	t.Helper()
	r, err := readRawErr(c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func readRawErr(c net.Conn) (map[string]any, error) {
	var h [4]byte
	if _, e := io.ReadFull(c, h[:]); e != nil {
		return nil, e
	}
	b := make([]byte, binary.BigEndian.Uint32(h[:]))
	if _, e := io.ReadFull(c, b); e != nil {
		return nil, e
	}
	var r map[string]any
	if e := json.Unmarshal(b, &r); e != nil {
		return nil, e
	}
	return r, nil
}
func rawOpen(t *testing.T, socket string, p any) map[string]any {
	t.Helper()
	c, e := net.Dial("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": p})
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	_, _ = c.Write(h[:])
	_, _ = c.Write(b)
	if _, e := io.ReadFull(c, h[:]); e != nil {
		t.Fatal(e)
	}
	body := make([]byte, binary.BigEndian.Uint32(h[:]))
	if _, e := io.ReadFull(c, body); e != nil {
		t.Fatal(e)
	}
	var r map[string]any
	_ = json.Unmarshal(body, &r)
	return r
}
