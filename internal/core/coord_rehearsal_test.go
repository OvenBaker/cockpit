package core_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The deterministic isolated rehearsal: one complete structured task ->
// builder handoff -> exact-SHA review -> acceptance -> release cycle driven
// through a real daemon process, the ctl transport, a throwaway tmux server,
// an isolated git repository, and a fake seeded launcher speaking the pinned
// four-flag interface. Terminal text never participates; the test injects
// plausible completion text into a pane mid-cycle and proves the projection
// ignores it.

type coordFixture struct {
	t                       *testing.T
	root, socket, tmux, bin string
	repo, worktree          string
	baseSha                 string
	launcherLog             string
	credentialsFile         string
	launcher                string
	daemon                  *exec.Cmd
	creds                   map[string]string    // role -> credential file
	identities              map[string][2]string // role -> {client id, profile}
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newCoordFixture(t *testing.T) *coordFixture {
	t.Helper()
	root := t.TempDir()
	f := &coordFixture{t: t, root: root, socket: filepath.Join(root, "control.sock")}
	f.tmux = "cp-it-coord-" + fmt.Sprint(time.Now().UnixNano())
	run(t, "tmux", "-L", f.tmux, "new-session", "-d", "-s", "slice", "-n", "work", "sleep 600")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", f.tmux, "kill-server").Run() })

	f.bin = filepath.Join(root, "cockpit-core")
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	cmd := exec.Command("go", "build", "-tags", "cockpit_test", "-buildvcs=false", "-o", f.bin, "./cmd/cockpit-core")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOCACHE=/tmp/cp-core-go-cache")
	if o, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build: %v: %s", e, o)
	}

	// Isolated git repository and builder worktree with one real commit.
	f.repo = filepath.Join(root, "repo")
	run(t, "git", "init", "-q", "-b", "main", f.repo)
	git := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = f.repo
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		o, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v: %s", args, e, o)
		}
		return strings.TrimSpace(string(o))
	}
	if err := os.WriteFile(filepath.Join(f.repo, "base.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "base")
	f.baseSha = git("rev-parse", "HEAD")
	f.worktree = filepath.Join(root, "wt")
	git("worktree", "add", "-q", "-b", "feat/test", f.worktree, "main")

	// Fake seeded launcher: records argv, verifies the prompt material, and
	// prints a pane id. It is the only launch path; no send-keys exists.
	f.launcherLog = filepath.Join(root, "launcher.log")
	launcher := filepath.Join(root, "fake-seed-spawn")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> ` + f.launcherLog + `
declare -A a; while [[ $# -gt 0 ]]; do a["$1"]="$2"; shift 2; done
f="${a[--initial-prompt-file]}"
[[ -f "$f" && ! -L "$f" ]] || { echo bad-file >&2; exit 4; }
[[ "$(sha256sum < "$f" | cut -d' ' -f1)" == "${a[--initial-prompt-sha256]}" ]] || { echo drift >&2; exit 4; }
[[ "$(wc -c < "$f")" -eq "${a[--initial-prompt-bytes]}" ]] || { echo bytes >&2; exit 4; }
echo '%77'
`
	if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	// Credentials: three role clients with grant-pinned client ids, plus a
	// local operator for workstream creation.
	registry := map[string]any{"version": 1, "clients": []any{
		map[string]any{"credential": "cred-op", "clientId": "operator-client", "profile": "local-operator", "capabilities": []string{"state:read", "coord:admin", "coord:read", "coord:write"}},
		map[string]any{"credential": "cred-orch", "clientId": "orch-client", "profile": "local-operator", "capabilities": []string{"state:read", "coord:read", "coord:write"}},
		map[string]any{"credential": "cred-builder", "clientId": "builder-client", "profile": "mcp-local", "capabilities": []string{"state:read", "coord:read", "coord:write"}},
		map[string]any{"credential": "cred-reviewer", "clientId": "reviewer-client", "profile": "mcp-local", "capabilities": []string{"state:read", "coord:read", "coord:write"}},
		map[string]any{"credential": "cred-floating", "profile": "read-only", "capabilities": []string{"state:read", "coord:read"}},
	}}
	rb, _ := json.Marshal(registry)
	credentials := filepath.Join(root, "clients.json")
	if err := os.WriteFile(credentials, rb, 0600); err != nil {
		t.Fatal(err)
	}
	f.creds = map[string]string{}
	f.identities = map[string][2]string{
		"operator":     {"operator-client", "local-operator"},
		"orchestrator": {"orch-client", "local-operator"},
		"builder":      {"builder-client", "mcp-local"},
		"reviewer":     {"reviewer-client", "mcp-local"},
		"floating":     {"floating-client", "read-only"},
	}
	for role, cred := range map[string]string{"operator": "cred-op", "orchestrator": "cred-orch", "builder": "cred-builder", "reviewer": "cred-reviewer", "floating": "cred-floating"} {
		p := filepath.Join(root, role+".token")
		if err := os.WriteFile(p, []byte(cred), 0600); err != nil {
			t.Fatal(err)
		}
		f.creds[role] = p
	}
	f.credentialsFile = credentials
	f.launcher = launcher
	f.start()
	return f
}

func (f *coordFixture) start() {
	f.t.Helper()
	c := exec.Command(f.bin, "daemon", "--test-root", f.root, "--socket", f.socket, "--tmux-socket", f.tmux, "--credentials-file", f.credentialsFile)
	c.Env = append(os.Environ(), "COCKPIT_SEED_LAUNCHER="+f.launcher)
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

func (f *coordFixture) stop() {
	if f.daemon != nil && f.daemon.Process != nil {
		_ = f.daemon.Process.Kill()
		_ = f.daemon.Wait()
		f.daemon = nil
	}
	_ = os.Remove(f.socket)
}

func (f *coordFixture) ctl(role, method string, params any) map[string]any {
	f.t.Helper()
	p, _ := json.Marshal(params)
	id := f.identities[role]
	c := exec.Command(f.bin, "ctl", "--socket", f.socket, "--credential-file", f.creds[role], "--client-id", id[0], "--profile", id[1], method, string(p))
	o, e := c.Output()
	if e != nil {
		f.t.Fatalf("ctl %s: %v", method, e)
	}
	var r map[string]any
	if e = json.Unmarshal(o, &r); e != nil {
		f.t.Fatal(e)
	}
	return r
}

func (f *coordFixture) result(role, method string, params any) map[string]any {
	f.t.Helper()
	r := f.ctl(role, method, params)
	res, ok := r["result"].(map[string]any)
	if !ok {
		f.t.Fatalf("%s: %#v", method, r)
	}
	return res
}

func (f *coordFixture) errCode(role, method string, params any) string {
	f.t.Helper()
	r := f.ctl(role, method, params)
	e, ok := r["error"].(map[string]any)
	if !ok {
		f.t.Fatalf("%s expected error: %#v", method, r)
	}
	return e["message"].(string)
}

func (f *coordFixture) revision(ws string) float64 {
	return f.result("orchestrator", "coordination.status_get", map[string]any{"workstreamId": ws})["revision"].(float64)
}

func ikc(n int) string { return fmt.Sprintf("ik_%d_%032x", time.Now().Unix(), 7000+n) }

func TestCoordRehearsalFullCycle(t *testing.T) {
	f := newCoordFixture(t)
	defer f.stop()
	ws := "coord"
	planSha := strings.Repeat("c", 64)

	// Workstream creation binds roles server-side.
	f.result("operator", "coordination.workstream_create", map[string]any{
		"workstreamId": ws, "idempotencyKey": ikc(1),
		"record": map[string]any{
			"schemaVersion": "coord.workstream-contract.v0", "recordType": "workstream-contract",
			"workstreamId": ws, "createdAt": "2026-07-22T12:00:00Z", "createdByRole": "operator",
			"repository": f.repo, "description": "rehearsal",
			"roles": []map[string]any{
				{"role": "orchestrator", "clientId": "orch-client"},
				{"role": "builder", "clientId": "builder-client"},
				{"role": "reviewer", "clientId": "reviewer-client"},
			},
		},
	})

	assignment := map[string]any{
		"schemaVersion": "coord.task-assignment.v0", "recordType": "task-assignment",
		"workstreamId": ws, "taskId": "BUILD-001", "revision": 0, "status": "published",
		"createdAt": "2026-07-22T12:00:00Z", "createdByRole": "orchestrator", "assignedRole": "builder",
		"assignee":  map[string]any{"provider": "claude", "model": "m", "effort": "high", "approvalMode": "auto"},
		"authority": map[string]any{"repository": f.repo, "repositoryAuthority": "sole-writer", "worktree": f.worktree, "branch": "feat/test", "allowedWrites": []string{f.worktree}, "forbiddenWrites": []string{f.repo}, "may": []string{"commit"}, "mayNot": []string{"merge"}, "writeLease": map[string]any{"leaseId": "LEASE-BUILD-001-r0", "scope": f.worktree, "holderRole": "builder", "exclusive": true, "acquireOnStructuredAcknowledgement": true, "releaseOnValidHandoff": true}},
		"pins": map[string]any{
			"plan": map[string]any{"path": "/tmp/plan.json", "sha256": planSha, "revision": 0},
			"base": map[string]any{"ref": "main", "refreshedAt": "2026-07-22T12:00:00Z", "sha": f.baseSha},
		},
		"objective":              "rehearsal objective",
		"scope":                  map[string]any{"required": []string{"r"}, "explicitlyOutOfScope": []string{"o"}},
		"implementationGuidance": []string{"g"}, "acceptanceCriteria": []map[string]any{{"id": "B1", "criterion": "c"}},
		"focusedVerification": []string{"go test"},
		"requiredOutputs":     map[string]any{"repository": []string{"commits"}, "handoff": map[string]any{"delivery": "structured", "requiredFields": []string{"taskId"}}},
		"stopConditions":      []string{"stop"},
	}
	pub := f.result("orchestrator", "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(2), "record": assignment,
	})
	assignmentSha := pub["assignmentSha256"].(string)

	// Put the throwaway pane into copy mode with scrolled output before
	// delivery: provider-native prompt-file delivery must be indifferent.
	run(t, "tmux", "-L", f.tmux, "send-keys", "-t", "slice:0.0", "-l", "scrollback noise")
	run(t, "tmux", "-L", f.tmux, "copy-mode", "-t", "slice:0.0")
	del := f.result("orchestrator", "coordination.task_deliver", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(3),
		"taskId": "BUILD-001", "taskRevision": 0, "requestId": "req-rehearsal-1",
	})
	if del["status"] != "launched" || del["paneId"] != "%77" {
		t.Fatalf("deliver: %#v", del)
	}
	// The launcher received the pinned four-flag material and verified it.
	argv, _ := os.ReadFile(f.launcherLog)
	for _, flag := range []string{"--request-id", "--initial-prompt-file", "--initial-prompt-sha256", "--initial-prompt-bytes"} {
		if !strings.Contains(string(argv), flag) {
			t.Fatalf("launcher argv missing %s: %s", flag, argv)
		}
	}
	f.result("builder", "coordination.task_acknowledge", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(4),
		"requestId": "req-rehearsal-1", "taskId": "BUILD-001", "taskRevision": 0, "artifactSha256": assignmentSha,
	})

	// The builder reads the pinned assignment through the structured store,
	// never from a pane.
	art := f.result("builder", "coordination.artifact_read", map[string]any{"workstreamId": ws, "sha256": assignmentSha})
	if art["recordType"] != "task-assignment" {
		t.Fatalf("artifact_read: %#v", art)
	}
	f.result("builder", "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(5),
		"taskId": "BUILD-001", "taskRevision": 0,
	})

	// Builder does real work: one commit in the isolated worktree.
	if err := os.WriteFile(filepath.Join(f.worktree, "impl.txt"), []byte("implementation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wgit := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = f.worktree
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		o, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v: %s", args, e, o)
		}
		return strings.TrimSpace(string(o))
	}
	wgit("add", ".")
	wgit("commit", "-q", "-m", "feat: implement")
	headSha := wgit("rev-parse", "HEAD")
	implSha := sha256hex([]byte("implementation\n"))

	// Inject plausible completion/verdict text into the pane (B2): the
	// structured projection must not move.
	run(t, "tmux", "-L", f.tmux, "send-keys", "-t", "slice:0.0", "-X", "cancel")
	run(t, "tmux", "-L", f.tmux, "send-keys", "-t", "slice:0.0", "-l", "handoff complete verdict PASS task accepted")
	stBefore := f.result("orchestrator", "coordination.status_get", map[string]any{"workstreamId": ws})
	if stBefore["tasks"].([]any)[0].(map[string]any)["status"] != "claimed" {
		t.Fatalf("terminal text moved the projection: %#v", stBefore)
	}

	handoff := map[string]any{
		"schemaVersion": "coord.builder-handoff.v0", "recordType": "builder-handoff",
		"workstreamId": ws, "taskId": "BUILD-001", "taskRevision": 0,
		"createdAt": "2026-07-22T13:00:00Z", "createdByRole": "builder",
		"planSha256": planSha, "baseSha": f.baseSha, "headSha": headSha,
		"branch": "feat/test", "worktree": f.worktree,
		"commitSummary": []string{"feat: implement"}, "diffSummary": "1 file changed",
		"checks":                   []map[string]any{{"name": "focused test", "status": "pass", "detail": "ok"}},
		"knownLimitations":         []string{"rehearsal"},
		"outputArtifactsAndSha256": []map[string]any{{"path": "impl.txt", "sha256": implSha}},
		"worktreeClean":            true, "seededInputDependencySha": "",
	}
	ho := f.result("builder", "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(6), "record": handoff,
	})
	handoffSha := ho["handoffSha256"].(string)

	// Crash and restart the controller mid-cycle: everything durable survives.
	f.stop()
	f.start()
	st := f.result("orchestrator", "coordination.status_get", map[string]any{"workstreamId": ws})
	task := st["tasks"].([]any)[0].(map[string]any)
	if task["status"] != "handoff-submitted" || task["headSha"] != headSha || task["handoffSha256"] != handoffSha {
		t.Fatalf("post-restart projection: %#v", task)
	}
	// Idempotent replay across process restart returns the original result.
	replay := f.result("builder", "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws) - 1, "idempotencyKey": ikc(6), "record": handoff,
	})
	if replay["replayed"] != true || replay["handoffSha256"] != handoffSha {
		t.Fatalf("cross-restart replay: %#v", replay)
	}

	rr := f.result("orchestrator", "coordination.review_request", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(7),
		"record": map[string]any{
			"schemaVersion": "coord.review-request.v0", "recordType": "review-request",
			"workstreamId": ws, "taskId": "BUILD-001", "taskRevision": 0,
			"createdAt": "2026-07-22T14:00:00Z", "createdByRole": "orchestrator",
			"handoffSha256": handoffSha, "headSha": headSha, "baseSha": f.baseSha,
			"reviewScope": []string{"full delta"},
		},
	})
	requestSha := rr["reviewRequestSha256"].(string)

	// CLI/MCP parity: the same wrong-role verdict is denied identically on
	// both transports, and the MCP read tool returns the same projection.
	verdict := map[string]any{
		"schemaVersion": "coord.review-result.v0", "recordType": "review-result",
		"workstreamId": ws, "taskId": "BUILD-001", "taskRevision": 0,
		"createdAt": "2026-07-22T15:00:00Z", "createdByRole": "reviewer",
		"reviewRequestSha256": requestSha, "handoffSha256": handoffSha, "headSha": headSha,
		"verdict": "PASS", "findings": []map[string]any{}, "recommendedDisposition": "accept",
	}
	wrongRole := f.errCode("builder", "coordination.review_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(8), "record": verdict,
	})
	if wrongRole != "PERMISSION_DENIED" {
		t.Fatalf("ctl wrong-role verdict: %s", wrongRole)
	}
	f.mcpParity(t, ws, verdict)

	rs := f.result("reviewer", "coordination.review_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(9), "record": verdict,
	})
	resultSha := rs["reviewResultSha256"].(string)

	acc := f.result("orchestrator", "coordination.acceptance_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(10),
		"record": map[string]any{
			"schemaVersion": "coord.final-acceptance.v0", "recordType": "final-acceptance",
			"workstreamId": ws, "taskId": "BUILD-001", "taskRevision": 0,
			"createdAt": "2026-07-22T16:00:00Z", "createdByRole": "orchestrator",
			"headSha": headSha, "handoffSha256": handoffSha, "reviewResultSha256": resultSha,
			"gates":            []map[string]any{{"name": "focused test", "status": "pass", "detail": ""}},
			"artifactManifest": []map[string]any{{"path": "impl.txt", "sha256": implSha}},
		},
	})
	f.result("orchestrator", "coordination.release_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(11),
		"record": map[string]any{
			"schemaVersion": "coord.release-handoff.v0", "recordType": "release-handoff",
			"workstreamId": ws, "taskId": "BUILD-001", "taskRevision": 0,
			"createdAt": "2026-07-22T17:00:00Z", "createdByRole": "orchestrator",
			"acceptanceSha256": acc["acceptanceSha256"].(string), "headSha": headSha, "baseSha": f.baseSha,
			"branch": "feat/test", "notes": []string{"rehearsal complete"},
		},
	})

	// Release-conductor style consumption: bounded reads only, exact SHAs.
	final := f.result("floating", "coordination.status_get", map[string]any{"workstreamId": ws})
	task = final["tasks"].([]any)[0].(map[string]any)
	if task["status"] != "released" || task["headSha"] != headSha {
		t.Fatalf("final projection: %#v", task)
	}
	ev := f.result("floating", "coordination.events_list", map[string]any{"workstreamId": ws, "afterSeq": 0, "limit": 100})
	if len(ev["events"].([]any)) < 10 {
		t.Fatalf("event log incomplete: %#v", ev)
	}
	// The read-only floating client (no pinned identity, no role) can never
	// mutate — even claiming to be the builder.
	denied := f.errCode("floating", "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(12),
		"taskId": "BUILD-001", "taskRevision": 0,
	})
	if denied != "PERMISSION_DENIED" {
		t.Fatalf("floating client mutation: %s", denied)
	}

	// The entire cycle ran without a single send-keys interaction from the
	// controller: the driver trace records no typed key delivery.
	trace, _ := os.ReadFile(filepath.Join(f.root, "driver.trace"))
	if strings.Contains(string(trace), "send-keys") {
		t.Fatalf("controller drove keys during coordination: %s", trace)
	}
}

// mcpParity proves MCP and ctl share result/error semantics for coordination.
func (f *coordFixture) mcpParity(t *testing.T, ws string, verdict map[string]any) {
	t.Helper()
	credFile := filepath.Join(f.root, "mcp-builder.token")
	if err := os.WriteFile(credFile, []byte("cred-builder"), 0600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(f.bin, "mcp-stdio", "--socket", f.socket)
	c.Env = append(os.Environ(), "COCKPIT_MCP_CREDENTIAL_FILE="+credFile)
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 4096), 1<<20)
	send := func(v any) {
		b, _ := json.Marshal(v)
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	recv := func() map[string]any {
		if !sc.Scan() {
			t.Fatalf("mcp closed: %v", sc.Err())
		}
		var r map[string]any
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	recv()
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	tools := recv()["result"].(map[string]any)["tools"].([]any)
	seen := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		seen[name] = true
		if name == "coord_status" && tool["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Fatal("coord_status not annotated read-only")
		}
	}
	for _, want := range []string{"coord_task_publish", "coord_task_claim", "coord_task_acknowledge", "coord_artifact_read", "coord_artifact_publish", "coord_handoff_submit", "coord_review_request", "coord_review_submit", "coord_acceptance_submit", "coord_release_submit", "coord_status", "coord_events", "coord_wait"} {
		if !seen[want] {
			t.Fatalf("MCP tool %s missing", want)
		}
	}
	// Same projection through MCP as through ctl.
	send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "coord_status", "arguments": map[string]any{"workstreamId": ws}}})
	res := recv()["result"].(map[string]any)
	mcpStatus := res["structuredContent"].(map[string]any)
	ctlStatus := f.result("builder", "coordination.status_get", map[string]any{"workstreamId": ws})
	mb, _ := json.Marshal(mcpStatus)
	cb, _ := json.Marshal(ctlStatus)
	if string(mb) != string(cb) {
		t.Fatalf("MCP/ctl status divergence:\n%s\n%s", mb, cb)
	}
	// Same domain error code for the same denied mutation (builder submitting
	// a review verdict), and no transition from the malformed follow-up.
	send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "coord_review_submit", "arguments": map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(40), "record": verdict,
	}}})
	errText := recv()["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(errText, "PERMISSION_DENIED") {
		t.Fatalf("MCP wrong-role verdict: %s", errText)
	}
	// Malformed MCP output (truncated record) cannot cause a transition.
	send(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{"name": "coord_handoff_submit", "arguments": map[string]any{
		"workstreamId": ws, "expectedRevision": f.revision(ws), "idempotencyKey": ikc(41), "record": map[string]any{"recordType": "builder-handoff", "truncated": true},
	}}})
	errText = recv()["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(errText, "INVALID_REQUEST") && !strings.Contains(errText, "PERMISSION_DENIED") {
		t.Fatalf("malformed MCP record: %s", errText)
	}
}
