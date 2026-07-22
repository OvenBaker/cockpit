package coord

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLauncher stands in for the pinned external seeded-spawn producer. It
// records the exact four-flag material it was handed and is idempotent per
// request id like the real contract.
type fakeLauncher struct {
	calls []LaunchRequest
	err   error
	pane  string
}

func (f *fakeLauncher) Launch(req LaunchRequest) (string, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return "", f.err
	}
	if f.pane == "" {
		f.pane = "%42"
	}
	return f.pane, nil
}

func deliverParams(t *testing.T, s *Service, ws, taskID, requestID string) map[string]any {
	return map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"taskId": taskID, "taskRevision": 0, "requestId": requestID,
	}
}

func TestDeliverAndAcknowledgeBoundByHash(t *testing.T) {
	fake := &fakeLauncher{}
	s, db, root := newTestService(t, fake)
	ws, taskID := "coord", "BUILD-001"
	setupWorkstream(t, s, ws)
	assignmentSha := publishTask(t, s, ws, taskID, "/tmp/wt")

	d := call(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, taskID, "req-1"))
	if d["status"] != "launched" || d["paneId"] != "%42" {
		t.Fatalf("deliver: %#v", d)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("launcher calls = %d", len(fake.calls))
	}
	req := fake.calls[0]
	if req.RequestID != "req-1" || req.Cwd != "/tmp/wt" || req.PromptBytes == 0 {
		t.Fatalf("launch request: %#v", req)
	}
	// The prompt file is private and carries only the typed pointer envelope.
	info, err := os.Lstat(req.PromptFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("prompt file: %v %v", info, err)
	}
	b, _ := os.ReadFile(req.PromptFile)
	if !strings.HasPrefix(string(b), "coord.task-pointer.v0 ") || !strings.Contains(string(b), "artifactHash="+assignmentSha) {
		t.Fatalf("envelope: %q", b)
	}
	if sha256Hex(b) != req.PromptSha256 || int64(len(b)) != req.PromptBytes {
		t.Fatal("launch material does not match prompt file bytes")
	}
	if !strings.HasPrefix(req.PromptFile, filepath.Join(root, "coord", "seeds")) {
		t.Fatalf("prompt staged outside private seed dir: %s", req.PromptFile)
	}

	// A wrong-hash acknowledgement is refused without mutation (B3).
	before := snapshot(t, db)
	wantErr(t, s, builder, "coordination.task_acknowledge", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"requestId": "req-1", "taskId": taskID, "taskRevision": 0,
		"artifactSha256": strings.Repeat("0", 64),
	}, "CONFLICT_MATERIAL_STATE")
	// So is an acknowledgement from the wrong role or for an unknown request.
	wantErr(t, s, orch, "coordination.task_acknowledge", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"requestId": "req-1", "taskId": taskID, "taskRevision": 0, "artifactSha256": assignmentSha,
	}, "PERMISSION_DENIED")
	wantErr(t, s, builder, "coordination.task_acknowledge", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"requestId": "req-unknown", "taskId": taskID, "taskRevision": 0, "artifactSha256": assignmentSha,
	}, "TARGET_NOT_FOUND")
	if after := snapshot(t, db); after != before {
		t.Fatal("refused acknowledgements mutated state")
	}

	ack := call(t, s, builder, "coordination.task_acknowledge", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"requestId": "req-1", "taskId": taskID, "taskRevision": 0, "artifactSha256": assignmentSha,
	})
	if ack["status"] != "acknowledged" {
		t.Fatalf("ack: %#v", ack)
	}
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	if st["tasks"].([]any)[0].(map[string]any)["status"] != "acknowledged" {
		t.Fatalf("task status: %#v", st)
	}
	if st["deliveries"].([]any)[0].(map[string]any)["status"] != "acknowledged" {
		t.Fatalf("delivery status: %#v", st)
	}
}

func TestDeliverReplayAndMaterialConflict(t *testing.T) {
	fake := &fakeLauncher{}
	s, db, _ := newTestService(t, fake)
	ws := "coord"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, "BUILD-001", "/tmp/wt")
	publishTask(t, s, ws, "BUILD-002", "/tmp/wt2")

	first := call(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-1"))
	// Redelivery with the same request id and material reconciles to the same
	// pane through the idempotent launcher; only one delivery row exists.
	again := call(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-1"))
	if again["paneId"] != first["paneId"] {
		t.Fatalf("replay changed pane: %#v vs %#v", again, first)
	}
	var rows int
	if err := db.QueryRow("SELECT count(*) FROM coord_deliveries").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("delivery rows = %d", rows)
	}
	// The same request id with different material conflicts before any launch.
	callsBefore := len(fake.calls)
	before := snapshot(t, db)
	wantErr(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-002", "req-1"), "CONFLICT_MATERIAL_STATE")
	if len(fake.calls) != callsBefore {
		t.Fatal("conflicting delivery reached the launcher")
	}
	if after := snapshot(t, db); after != before {
		t.Fatal("conflicting delivery mutated state")
	}
}

func TestDeliverIdempotencyKeySemantics(t *testing.T) {
	fake := &fakeLauncher{}
	s, db, _ := newTestService(t, fake)
	ws := "coord"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, "BUILD-001", "/tmp/wt")
	publishTask(t, s, ws, "BUILD-002", "/tmp/wt2")
	key := ikey()
	expected := rev(t, s, ws)
	params := map[string]any{
		"workstreamId": ws, "expectedRevision": expected, "idempotencyKey": key,
		"taskId": "BUILD-001", "taskRevision": 0, "requestId": "req-idem",
	}
	first := call(t, s, orch, "coordination.task_deliver", params)
	if first["status"] != "launched" {
		t.Fatalf("deliver: %#v", first)
	}
	// Same key + same intent replays the settled launched outcome without a
	// second launch.
	calls := len(fake.calls)
	replay := call(t, s, orch, "coordination.task_deliver", params)
	if replay["replayed"] != true || replay["status"] != "launched" || replay["paneId"] != first["paneId"] {
		t.Fatalf("replay: %#v", replay)
	}
	if len(fake.calls) != calls {
		t.Fatal("replay reached the launcher")
	}
	// Same key + different intent fails without mutation (B5).
	before := snapshot(t, db)
	wantErr(t, s, orch, "coordination.task_deliver", map[string]any{
		"workstreamId": ws, "expectedRevision": expected, "idempotencyKey": key,
		"taskId": "BUILD-002", "taskRevision": 0, "requestId": "req-other",
	}, "IDEMPOTENCY_CONFLICT")
	if after := snapshot(t, db); after != before {
		t.Fatal("idempotency conflict mutated state")
	}
}

func TestTerminalLaunchFailureIsDurable(t *testing.T) {
	fake := &fakeLauncher{err: &LaunchError{Terminal: true, Code: "seed-terminal-failure", Detail: "pane gone"}}
	s, _, _ := newTestService(t, fake)
	ws := "coord"
	setupWorkstream(t, s, ws)
	assignmentSha := publishTask(t, s, ws, "BUILD-001", "/tmp/wt")
	wantErr(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-1"), "CONFLICT_MATERIAL_STATE")
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	if st["deliveries"].([]any)[0].(map[string]any)["status"] != "failed" {
		t.Fatalf("delivery: %#v", st["deliveries"])
	}
	// A failed delivery cannot be re-launched or acknowledged.
	wantErr(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-1"), "CONFLICT_MATERIAL_STATE")
	wantErr(t, s, builder, "coordination.task_acknowledge", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"requestId": "req-1", "taskId": "BUILD-001", "taskRevision": 0, "artifactSha256": assignmentSha,
	}, "CONFLICT_MATERIAL_STATE")
}

func TestTransientLaunchFailureStaysRedeliverable(t *testing.T) {
	fake := &fakeLauncher{err: &LaunchError{Terminal: false, Code: "seed-launcher-unavailable", Detail: "no grid"}}
	s, _, _ := newTestService(t, fake)
	ws := "coord"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, "BUILD-001", "/tmp/wt")
	wantErr(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-1"), "CONTROLLER_NOT_READY")
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	if st["deliveries"].([]any)[0].(map[string]any)["status"] != "prepared" {
		t.Fatalf("delivery: %#v", st["deliveries"])
	}
	// After the environment recovers, the same reservation launches once.
	fake.err = nil
	d := call(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-1"))
	if d["status"] != "launched" {
		t.Fatalf("recovered delivery: %#v", d)
	}
}

func TestNoLauncherFailsClosed(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	ws := "coord"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, "BUILD-001", "/tmp/wt")
	wantErr(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-1"), "CAPABILITY_ABSENT")
}

func TestPromptFileSymlinkAndDriftFailClosed(t *testing.T) {
	fake := &fakeLauncher{}
	s, _, root := newTestService(t, fake)
	ws := "coord"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, "BUILD-001", "/tmp/wt")

	// A pre-planted symlink at the prompt path is refused outright.
	seedDir := filepath.Join(root, "coord", "seeds")
	if err := os.MkdirAll(seedDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(seedDir, "req-sym.prompt")); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-sym"), "CONTROLLER_NOT_READY")
	if len(fake.calls) != 0 {
		t.Fatal("symlinked prompt reached the launcher")
	}

	// Reserve with a transient failure, tamper the staged prompt, and prove
	// redelivery refuses the drifted material without launching.
	fake.err = &LaunchError{Terminal: false, Code: "seed-launcher-unavailable", Detail: "down"}
	wantErr(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-2"), "CONTROLLER_NOT_READY")
	prompt := filepath.Join(seedDir, "req-2.prompt")
	if err := os.WriteFile(prompt, []byte("coord.task-pointer.v0 tampered=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fake.err = nil
	calls := len(fake.calls)
	wantErr(t, s, orch, "coordination.task_deliver", deliverParams(t, s, ws, "BUILD-001", "req-2"), "CONFLICT_MATERIAL_STATE")
	if len(fake.calls) != calls {
		t.Fatal("drifted prompt reached the launcher")
	}
}

func TestExecLauncherFlagContract(t *testing.T) {
	// The exec adapter must speak exactly the pinned material-binding flags
	// plus the launch context and the fixed agent interaction profile
	// (reviewed seeded-spawn head 86c544e), and map the producer's exit
	// codes. The exact-argv equality below is the proof that no other flag —
	// and never the human profile — can be requested.
	root := t.TempDir()
	script := filepath.Join(root, "fake-spawn")
	log := filepath.Join(root, "argv.log")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > "+log+"\necho '%7'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(root, "p.prompt")
	if err := os.WriteFile(prompt, []byte("coord.task-pointer.v0 x=y\n"), 0600); err != nil {
		t.Fatal(err)
	}
	l := ExecLauncher{Path: script}
	pane, err := l.Launch(LaunchRequest{Cwd: "/tmp/wt", Name: "BUILD-001", RequestID: "req-1", PromptFile: prompt, PromptSha256: strings.Repeat("a", 64), PromptBytes: 26})
	if err != nil || pane != "%7" {
		t.Fatalf("launch: %q %v", pane, err)
	}
	argv, _ := os.ReadFile(log)
	want := fmt.Sprintf("--cwd\n/tmp/wt\n--name\nBUILD-001\n--interaction-profile\nagent\n--request-id\nreq-1\n--initial-prompt-file\n%s\n--initial-prompt-sha256\n%s\n--initial-prompt-bytes\n26\n", prompt, strings.Repeat("a", 64))
	if string(argv) != want {
		t.Fatalf("argv:\n%q\nwant\n%q", argv, want)
	}
	for exit, wantCode := range map[int]string{2: "seed-validation-refused", 4: "seed-terminal-failure", 5: "seed-material-conflict"} {
		if err := os.WriteFile(script, fmt.Appendf(nil, "#!/usr/bin/env bash\nexit %d\n", exit), 0700); err != nil {
			t.Fatal(err)
		}
		_, err := l.Launch(LaunchRequest{RequestID: "r"})
		le, ok := err.(*LaunchError)
		if !ok || !le.Terminal || le.Code != wantCode {
			t.Fatalf("exit %d: %v", exit, err)
		}
	}
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = l.Launch(LaunchRequest{RequestID: "r"})
	le, ok := err.(*LaunchError)
	if !ok || le.Terminal {
		t.Fatalf("exit 1 should be transient: %v", err)
	}
}
