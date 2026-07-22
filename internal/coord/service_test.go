package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const (
	testBaseSha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHeadSha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testPlanSha = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testSeedSha = "dddddddddddddddddddddddddddddddddddddddd"
)

var (
	orch     = Session{Caller: "orch-client", Fixed: true, Profile: "local-operator"}
	builder  = Session{Caller: "builder-client", Fixed: true, Profile: "mcp-local"}
	reviewer = Session{Caller: "reviewer-client", Fixed: true, Profile: "mcp-local"}
	ctxT     = context.Background()
)

var ikCounter int

func ikey() string {
	ikCounter++
	return fmt.Sprintf("ik_%d_%032x", time.Now().Unix(), ikCounter)
}

func openTestDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "control.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db
}

// newTestService builds the coordination service over the controller's
// version-1 base schema in an isolated root. No tmux is involved anywhere.
func newTestService(t *testing.T, launcher Launcher) (*Service, *sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	db := openTestDB(t, root)
	if _, err := db.Exec(`CREATE TABLE meta(k TEXT PRIMARY KEY,v TEXT NOT NULL);
CREATE TABLE workspaces(ref TEXT PRIMARY KEY,window_id TEXT UNIQUE,name TEXT,generation INTEGER,version INTEGER);
CREATE TABLE panes(ref TEXT PRIMARY KEY,workspace_ref TEXT REFERENCES workspaces(ref),window_id TEXT,pane_id TEXT UNIQUE,generation INTEGER,version INTEGER,badge TEXT NOT NULL DEFAULT '',fenced INTEGER NOT NULL DEFAULT 0);
PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, root, launcher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return s, db, root
}

func call(t *testing.T, s *Service, sess Session, method string, params map[string]any) map[string]any {
	t.Helper()
	r, err := dispatch(s, sess, method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return r
}

func dispatch(s *Service, sess Session, method string, params map[string]any) (map[string]any, error) {
	b, _ := json.Marshal(params)
	v, err := s.Dispatch(ctxT, sess, method, b)
	if err != nil {
		return nil, err
	}
	rb, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(rb, &out)
	return out, nil
}

func wantErr(t *testing.T, s *Service, sess Session, method string, params map[string]any, code string) {
	t.Helper()
	_, err := dispatch(s, sess, method, params)
	ce, ok := err.(*Error)
	if !ok {
		t.Fatalf("%s: expected %s, got %v", method, code, err)
	}
	if ce.Code != code {
		t.Fatalf("%s: expected %s, got %s: %s", method, code, ce.Code, ce.Message)
	}
}

// snapshot digests the mutable coordination state so tests can assert that a
// refused operation mutated nothing.
func snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var parts []string
	for _, q := range []string{
		"SELECT COALESCE(MAX(revision),0) FROM coord_workstreams",
		"SELECT count(*) FROM coord_records",
		"SELECT count(*) FROM coord_events",
		"SELECT count(*) FROM coord_leases WHERE status='active'",
		"SELECT count(*) FROM coord_idempotency",
	} {
		var n int64
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprint(n))
	}
	rows, err := db.Query("SELECT task_id,revision,status,head_sha,handoff_sha,review_result_sha,acceptance_sha FROM coord_tasks ORDER BY task_id,revision")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status, head, handoff, review, acc string
		var rev int64
		if err = rows.Scan(&id, &rev, &status, &head, &handoff, &review, &acc); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%s:%s:%s:%s:%s", id, rev, status, head, handoff, review, acc))
	}
	return strings.Join(parts, "|")
}

func contractJSON(ws string) map[string]any {
	return map[string]any{
		"schemaVersion": "coord.workstream-contract.v0", "recordType": "workstream-contract",
		"workstreamId": ws, "createdAt": "2026-07-22T12:00:00Z", "createdByRole": "operator",
		"repository": "/tmp/repo", "description": "test workstream",
		"roles": []map[string]any{
			{"role": "orchestrator", "clientId": orch.Caller},
			{"role": "builder", "clientId": builder.Caller},
			{"role": "reviewer", "clientId": reviewer.Caller},
		},
	}
}

func assignmentJSON(ws, taskID string, revision int64, worktree string) map[string]any {
	a := map[string]any{
		"schemaVersion": "coord.task-assignment.v0", "recordType": "task-assignment",
		"workstreamId": ws, "taskId": taskID, "revision": revision, "status": "published",
		"createdAt": "2026-07-22T12:00:00Z", "createdByRole": "orchestrator", "assignedRole": "builder",
		"assignee": map[string]any{"provider": "claude", "model": "m", "effort": "high", "approvalMode": "auto"},
		"authority": map[string]any{
			"repository": "/tmp/repo", "repositoryAuthority": "sole-writer",
			"worktree": worktree, "branch": "feat/test",
			"allowedWrites": []string{worktree}, "forbiddenWrites": []string{"/tmp/repo"},
			"may": []string{"commit"}, "mayNot": []string{"merge"},
			"writeLease": map[string]any{
				"leaseId": "LEASE-" + taskID + fmt.Sprintf("-r%d", revision), "scope": worktree, "holderRole": "builder",
				"exclusive": true, "acquireOnStructuredAcknowledgement": true, "releaseOnValidHandoff": true,
			},
		},
		"pins": map[string]any{
			"plan":                  map[string]any{"path": "/tmp/plan.json", "sha256": testPlanSha, "revision": 0},
			"base":                  map[string]any{"ref": "origin/main", "refreshedAt": "2026-07-22T12:00:00Z", "sha": testBaseSha},
			"seededInputCapability": map[string]any{"ref": "origin/seed", "sha": testSeedSha, "interface": []string{"--request-id", "--initial-prompt-file", "--initial-prompt-sha256", "--initial-prompt-bytes"}, "integrationPolicy": "consume behind adapter"},
		},
		"objective":              "test objective",
		"scope":                  map[string]any{"required": []string{"r"}, "explicitlyOutOfScope": []string{"o"}},
		"implementationGuidance": []string{"g"},
		"acceptanceCriteria":     []map[string]any{{"id": "B1", "criterion": "c"}},
		"focusedVerification":    []string{"go test"},
		"requiredOutputs": map[string]any{
			"repository": []string{"commits"},
			"handoff":    map[string]any{"delivery": "structured", "requiredFields": []string{"taskId"}},
		},
		"stopConditions": []string{"stop after handoff"},
	}
	return a
}

func handoffJSON(ws, taskID string, revision int64, worktree string) map[string]any {
	return map[string]any{
		"schemaVersion": "coord.builder-handoff.v0", "recordType": "builder-handoff",
		"workstreamId": ws, "taskId": taskID, "taskRevision": revision,
		"createdAt": "2026-07-22T13:00:00Z", "createdByRole": "builder",
		"planSha256": testPlanSha, "baseSha": testBaseSha, "headSha": testHeadSha,
		"branch": "feat/test", "worktree": worktree,
		"commitSummary": []string{"feat: implement"}, "diffSummary": "2 files changed",
		"checks":                   []map[string]any{{"name": "go test ./...", "status": "pass", "detail": "ok"}},
		"knownLimitations":         []string{"none"},
		"outputArtifactsAndSha256": []map[string]any{{"path": "internal/x.go", "sha256": strings.Repeat("e", 64)}},
		"worktreeClean":            true,
		"seededInputDependencySha": testSeedSha,
	}
}

func setupWorkstream(t *testing.T, s *Service, ws string) {
	t.Helper()
	call(t, s, orch, "coordination.workstream_create", map[string]any{
		"workstreamId": ws, "idempotencyKey": ikey(), "record": contractJSON(ws),
	})
}

func rev(t *testing.T, s *Service, ws string) int64 {
	t.Helper()
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	return int64(st["revision"].(float64))
}

func publishTask(t *testing.T, s *Service, ws, taskID string, worktree string) string {
	t.Helper()
	r := call(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": assignmentJSON(ws, taskID, 0, worktree),
	})
	return r["assignmentSha256"].(string)
}

// ---- happy path: the full structured cycle -------------------------------

func TestFullStructuredCycle(t *testing.T) {
	s, db, _ := newTestService(t, nil)
	ws, taskID, worktree := "coord", "BUILD-001", "/tmp/wt"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, taskID, worktree)

	claim := call(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"taskId": taskID, "taskRevision": 0,
	})
	if claim["taskStatus"] != "claimed" || claim["leaseId"] != "LEASE-BUILD-001-r0" {
		t.Fatalf("claim: %#v", claim)
	}
	handoff := call(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": handoffJSON(ws, taskID, 0, worktree),
	})
	handoffSha := handoff["handoffSha256"].(string)
	if handoff["leaseReleased"] != true {
		t.Fatalf("lease not released on valid handoff: %#v", handoff)
	}
	reviewReq := call(t, s, orch, "coordination.review_request", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-request.v0", "recordType": "review-request",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T14:00:00Z", "createdByRole": "orchestrator",
			"handoffSha256": handoffSha, "headSha": testHeadSha, "baseSha": testBaseSha,
			"reviewScope": []string{"full delta"},
		},
	})
	requestSha := reviewReq["reviewRequestSha256"].(string)
	reviewRes := call(t, s, reviewer, "coordination.review_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-result.v0", "recordType": "review-result",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T15:00:00Z", "createdByRole": "reviewer",
			"reviewRequestSha256": requestSha, "handoffSha256": handoffSha, "headSha": testHeadSha,
			"verdict": "PASS", "findings": []map[string]any{},
			"recommendedDisposition": "accept",
		},
	})
	if reviewRes["taskStatus"] != "reviewed-pass" {
		t.Fatalf("review: %#v", reviewRes)
	}
	resultSha := reviewRes["reviewResultSha256"].(string)
	acceptance := call(t, s, orch, "coordination.acceptance_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.final-acceptance.v0", "recordType": "final-acceptance",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T16:00:00Z", "createdByRole": "orchestrator",
			"headSha": testHeadSha, "handoffSha256": handoffSha, "reviewResultSha256": resultSha,
			"gates":            []map[string]any{{"name": "go test", "status": "pass", "detail": ""}},
			"artifactManifest": []map[string]any{{"path": "internal/x.go", "sha256": strings.Repeat("e", 64)}},
		},
	})
	acceptanceSha := acceptance["acceptanceSha256"].(string)
	release := call(t, s, orch, "coordination.release_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.release-handoff.v0", "recordType": "release-handoff",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T17:00:00Z", "createdByRole": "orchestrator",
			"acceptanceSha256": acceptanceSha, "headSha": testHeadSha, "baseSha": testBaseSha,
			"branch": "feat/test", "notes": []string{"ready for release-conductor"},
		},
	})
	if release["taskStatus"] != "released" {
		t.Fatalf("release: %#v", release)
	}

	// The event log carries the entire cycle in order.
	ev := call(t, s, orch, "coordination.events_list", map[string]any{"workstreamId": ws, "afterSeq": 0, "limit": 50})
	var kinds []string
	for _, e := range ev["events"].([]any) {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	want := []string{"workstream.created", "task.published", "task.claimed", "lease.acquired", "handoff.submitted", "lease.released", "review.requested", "review.submitted", "task.accepted", "release.submitted"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}

	// Compact status carries the exact hash chain.
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	task := st["tasks"].([]any)[0].(map[string]any)
	if task["status"] != "released" || task["headSha"] != testHeadSha || task["handoffSha256"] != handoffSha || task["acceptanceSha256"] != acceptanceSha {
		t.Fatalf("status: %#v", task)
	}
	if st["activeLease"] != nil {
		t.Fatalf("lease leaked: %#v", st["activeLease"])
	}

	// Every published record is retrievable by exact hash with exact bytes.
	got := call(t, s, reviewer, "coordination.artifact_read", map[string]any{"workstreamId": ws, "sha256": handoffSha})
	if got["sha256"] != handoffSha || got["recordType"] != "builder-handoff" {
		t.Fatalf("artifact_read: %#v", got)
	}
	_ = db
}

// ---- fail-closed matrix (B5, B6) -----------------------------------------

func TestWrongRoleFailsClosedWithoutMutation(t *testing.T) {
	s, db, _ := newTestService(t, nil)
	ws, taskID, worktree := "coord", "BUILD-001", "/tmp/wt"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, taskID, worktree)
	before := snapshot(t, db)

	claimP := func() map[string]any {
		return map[string]any{"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "taskId": taskID, "taskRevision": 0}
	}
	// The orchestrator can never hold the builder lease (B6).
	wantErr(t, s, orch, "coordination.task_claim", claimP(), "PERMISSION_DENIED")
	// The reviewer cannot claim either.
	wantErr(t, s, reviewer, "coordination.task_claim", claimP(), "PERMISSION_DENIED")
	// A stranger holds no role.
	stranger := Session{Caller: "stranger", Fixed: true}
	wantErr(t, s, stranger, "coordination.task_claim", claimP(), "PERMISSION_DENIED")
	// A non-pinned identity cannot mutate even with the right name.
	imposter := Session{Caller: builder.Caller, Fixed: false}
	wantErr(t, s, imposter, "coordination.task_claim", claimP(), "PERMISSION_DENIED")
	// The builder cannot publish tasks.
	wantErr(t, s, builder, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": assignmentJSON(ws, "BUILD-002", 0, worktree),
	}, "PERMISSION_DENIED")
	if after := snapshot(t, db); after != before {
		t.Fatalf("refused operations mutated state:\n%s\n%s", before, after)
	}

	// Drive to handoff-submitted, then prove the orchestrator cannot submit a
	// review verdict (B6) and the builder cannot self-review.
	call(t, s, builder, "coordination.task_claim", claimP())
	call(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": handoffJSON(ws, taskID, 0, worktree),
	})
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	handoffSha := st["tasks"].([]any)[0].(map[string]any)["handoffSha256"].(string)
	call(t, s, orch, "coordination.review_request", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-request.v0", "recordType": "review-request",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T14:00:00Z", "createdByRole": "orchestrator",
			"handoffSha256": handoffSha, "headSha": testHeadSha, "baseSha": testBaseSha,
			"reviewScope": []string{},
		},
	})
	st = call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	requestSha := st["tasks"].([]any)[0].(map[string]any)["reviewRequestSha256"].(string)
	verdict := func(byRole string) map[string]any {
		return map[string]any{
			"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
			"record": map[string]any{
				"schemaVersion": "coord.review-result.v0", "recordType": "review-result",
				"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
				"createdAt": "2026-07-22T15:00:00Z", "createdByRole": byRole,
				"reviewRequestSha256": requestSha, "handoffSha256": handoffSha, "headSha": testHeadSha,
				"verdict": "PASS", "findings": []map[string]any{}, "recommendedDisposition": "accept",
			},
		}
	}
	before = snapshot(t, db)
	// Even with a forged createdByRole=reviewer payload, the orchestrator's
	// server-side role denies the mutation: payload roles are never trusted.
	wantErr(t, s, orch, "coordination.review_submit", verdict("reviewer"), "PERMISSION_DENIED")
	wantErr(t, s, builder, "coordination.review_submit", verdict("reviewer"), "PERMISSION_DENIED")
	// The reviewer cannot submit a verdict whose payload claims another role.
	wantErr(t, s, reviewer, "coordination.review_submit", verdict("orchestrator"), "INVALID_REQUEST")
	if after := snapshot(t, db); after != before {
		t.Fatalf("refused verdicts mutated state:\n%s\n%s", before, after)
	}
}

func TestStaleAndMismatchedTransitionsFailWithoutMutation(t *testing.T) {
	s, db, _ := newTestService(t, nil)
	ws, taskID, worktree := "coord", "BUILD-001", "/tmp/wt"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, taskID, worktree)
	call(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "taskId": taskID, "taskRevision": 0,
	})
	before := snapshot(t, db)

	// Stale expectedRevision.
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws) - 1, "idempotencyKey": ikey(),
		"record": handoffJSON(ws, taskID, 0, worktree),
	}, "CONFLICT_VERSION")

	// Wrong pinned hashes/base in the handoff.
	wrongPlan := handoffJSON(ws, taskID, 0, worktree)
	wrongPlan["planSha256"] = strings.Repeat("9", 64)
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": wrongPlan,
	}, "CONFLICT_MATERIAL_STATE")
	wrongBase := handoffJSON(ws, taskID, 0, worktree)
	wrongBase["baseSha"] = strings.Repeat("9", 40)
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": wrongBase,
	}, "CONFLICT_MATERIAL_STATE")
	wrongDep := handoffJSON(ws, taskID, 0, worktree)
	wrongDep["seededInputDependencySha"] = strings.Repeat("1", 40)
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": wrongDep,
	}, "CONFLICT_MATERIAL_STATE")
	emptyHandoff := handoffJSON(ws, taskID, 0, worktree)
	emptyHandoff["headSha"] = testBaseSha
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": emptyHandoff,
	}, "INVALID_REQUEST")
	if after := snapshot(t, db); after != before {
		t.Fatalf("refused handoffs mutated state:\n%s\n%s", before, after)
	}

	// Valid handoff, then mismatched review bindings.
	call(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": handoffJSON(ws, taskID, 0, worktree),
	})
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	handoffSha := st["tasks"].([]any)[0].(map[string]any)["handoffSha256"].(string)
	before = snapshot(t, db)
	reviewReq := func(handoff, head string) map[string]any {
		return map[string]any{
			"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
			"record": map[string]any{
				"schemaVersion": "coord.review-request.v0", "recordType": "review-request",
				"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
				"createdAt": "2026-07-22T14:00:00Z", "createdByRole": "orchestrator",
				"handoffSha256": handoff, "headSha": head, "baseSha": testBaseSha,
				"reviewScope": []string{},
			},
		}
	}
	// Wrong handoff hash and moved head both fail.
	wantErr(t, s, orch, "coordination.review_request", reviewReq(strings.Repeat("8", 64), testHeadSha), "CONFLICT_MATERIAL_STATE")
	wantErr(t, s, orch, "coordination.review_request", reviewReq(handoffSha, strings.Repeat("8", 40)), "CONFLICT_MATERIAL_STATE")
	if after := snapshot(t, db); after != before {
		t.Fatalf("refused review requests mutated state:\n%s\n%s", before, after)
	}

	call(t, s, orch, "coordination.review_request", reviewReq(handoffSha, testHeadSha))
	st = call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	requestSha := st["tasks"].([]any)[0].(map[string]any)["reviewRequestSha256"].(string)
	before = snapshot(t, db)
	verdict := func(request, head string) map[string]any {
		return map[string]any{
			"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
			"record": map[string]any{
				"schemaVersion": "coord.review-result.v0", "recordType": "review-result",
				"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
				"createdAt": "2026-07-22T15:00:00Z", "createdByRole": "reviewer",
				"reviewRequestSha256": request, "handoffSha256": handoffSha, "headSha": head,
				"verdict": "PASS", "findings": []map[string]any{}, "recommendedDisposition": "accept",
			},
		}
	}
	// A verdict for a different head or stale request hash is rejected.
	wantErr(t, s, reviewer, "coordination.review_submit", verdict(requestSha, strings.Repeat("7", 40)), "CONFLICT_MATERIAL_STATE")
	wantErr(t, s, reviewer, "coordination.review_submit", verdict(strings.Repeat("7", 64), testHeadSha), "CONFLICT_MATERIAL_STATE")
	if after := snapshot(t, db); after != before {
		t.Fatalf("refused verdicts mutated state:\n%s\n%s", before, after)
	}
}

func TestDuplicateLeaseAndClaimRaces(t *testing.T) {
	s, db, _ := newTestService(t, nil)
	ws, worktree := "coord", "/tmp/wt"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, "BUILD-001", worktree)
	claim := map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"taskId": "BUILD-001", "taskRevision": 0,
	}
	first := call(t, s, builder, "coordination.task_claim", claim)
	// Exact replay returns the original result without a second lease.
	replay := call(t, s, builder, "coordination.task_claim", claim)
	if replay["replayed"] != true || replay["leaseId"] != first["leaseId"] {
		t.Fatalf("claim replay: %#v", replay)
	}
	var active int
	if err := db.QueryRow("SELECT count(*) FROM coord_leases WHERE status='active'").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active leases = %d", active)
	}
	// A second claim attempt with fresh intent conflicts on the revision CAS,
	// and even with the current revision the already-claimed status refuses.
	wantErr(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"taskId": "BUILD-001", "taskRevision": 0,
	}, "CONFLICT_MATERIAL_STATE")
	// A second task cannot acquire a lease while the first is active.
	publishTask(t, s, ws, "BUILD-002", "/tmp/wt2")
	wantErr(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"taskId": "BUILD-002", "taskRevision": 0,
	}, "CONFLICT_MATERIAL_STATE")
}

func TestIdempotencyKeyReuseWithChangedIntent(t *testing.T) {
	s, db, _ := newTestService(t, nil)
	ws := "coord"
	setupWorkstream(t, s, ws)
	key := ikey()
	call(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": key,
		"record": assignmentJSON(ws, "BUILD-001", 0, "/tmp/wt"),
	})
	before := snapshot(t, db)
	// Same key, different record: refused without mutation.
	_, err := dispatch(s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": key,
		"record": assignmentJSON(ws, "BUILD-002", 0, "/tmp/wt2"),
	})
	ce, ok := err.(*Error)
	if !ok || ce.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("expected IDEMPOTENCY_CONFLICT, got %v", err)
	}
	if after := snapshot(t, db); after != before {
		t.Fatalf("idempotency conflict mutated state")
	}
	// Same key, same intent: replays even with a stale expectedRevision baked
	// into the original intent.
	replay := call(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": 1, "idempotencyKey": key,
		"record": assignmentJSON(ws, "BUILD-001", 0, "/tmp/wt"),
	})
	if replay["replayed"] != true || replay["taskId"] != "BUILD-001" {
		t.Fatalf("replay: %#v", replay)
	}
}

func TestLeaseTransferScopedSmallFix(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	ws, taskID, worktree := "coord", "BUILD-001", "/tmp/wt"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, taskID, worktree)
	call(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "taskId": taskID, "taskRevision": 0,
	})
	call(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": handoffJSON(ws, taskID, 0, worktree),
	})
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	handoffSha := st["tasks"].([]any)[0].(map[string]any)["handoffSha256"].(string)
	call(t, s, orch, "coordination.review_request", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-request.v0", "recordType": "review-request",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T14:00:00Z", "createdByRole": "orchestrator",
			"handoffSha256": handoffSha, "headSha": testHeadSha, "baseSha": testBaseSha, "reviewScope": []string{},
		},
	})
	transfer := func(scope []string) map[string]any {
		return map[string]any{
			"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
			"record": map[string]any{
				"schemaVersion": "coord.lease-transfer.v0", "recordType": "lease-transfer",
				"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
				"createdAt": "2026-07-22T15:00:00Z", "createdByRole": "orchestrator",
				"toRole": "reviewer", "scope": scope,
				"expiresAt": "2026-07-22T16:00:00Z", "reason": "typo",
			},
		}
	}
	// Scope escaping the worktree is refused.
	wantErr(t, s, orch, "coordination.lease_transfer", transfer([]string{"/etc/passwd"}), "INVALID_REQUEST")
	// The reviewer cannot self-approve a transfer.
	wantErr(t, s, reviewer, "coordination.lease_transfer", transfer([]string{worktree + "/x.go"}), "PERMISSION_DENIED")
	tr := call(t, s, orch, "coordination.lease_transfer", transfer([]string{worktree + "/x.go"}))
	leaseID := tr["leaseId"].(string)
	if tr["holderRole"] != "reviewer" || tr["holderClient"] != reviewer.Caller {
		t.Fatalf("transfer: %#v", tr)
	}
	st = call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	requestSha := st["tasks"].([]any)[0].(map[string]any)["reviewRequestSha256"].(string)
	verdict := map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-result.v0", "recordType": "review-result",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T15:30:00Z", "createdByRole": "reviewer",
			"reviewRequestSha256": requestSha, "handoffSha256": handoffSha, "headSha": testHeadSha,
			"verdict": "PASS", "findings": []map[string]any{}, "recommendedDisposition": "accept",
		},
	}
	// The verdict is blocked while the scoped lease is outstanding.
	wantErr(t, s, reviewer, "coordination.review_submit", verdict, "CONFLICT_MATERIAL_STATE")
	// Only the holder can return it.
	wantErr(t, s, builder, "coordination.lease_return", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "leaseId": leaseID,
	}, "PERMISSION_DENIED")
	call(t, s, reviewer, "coordination.lease_return", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "leaseId": leaseID,
	})
	verdict["expectedRevision"] = rev(t, s, ws)
	verdict["idempotencyKey"] = ikey()
	res := call(t, s, reviewer, "coordination.review_submit", verdict)
	if res["taskStatus"] != "reviewed-pass" {
		t.Fatalf("post-return verdict: %#v", res)
	}
}

func TestSingleCorrectionLoop(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	ws, taskID, worktree := "coord", "BUILD-001", "/tmp/wt"
	setupWorkstream(t, s, ws)
	publishTask(t, s, ws, taskID, worktree)
	call(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "taskId": taskID, "taskRevision": 0,
	})
	call(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": handoffJSON(ws, taskID, 0, worktree),
	})
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	handoffSha := st["tasks"].([]any)[0].(map[string]any)["handoffSha256"].(string)
	call(t, s, orch, "coordination.review_request", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-request.v0", "recordType": "review-request",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T14:00:00Z", "createdByRole": "orchestrator",
			"handoffSha256": handoffSha, "headSha": testHeadSha, "baseSha": testBaseSha, "reviewScope": []string{},
		},
	})
	st = call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	requestSha := st["tasks"].([]any)[0].(map[string]any)["reviewRequestSha256"].(string)
	res := call(t, s, reviewer, "coordination.review_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-result.v0", "recordType": "review-result",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T15:00:00Z", "createdByRole": "reviewer",
			"reviewRequestSha256": requestSha, "handoffSha256": handoffSha, "headSha": testHeadSha,
			"verdict":                "CHANGES_REQUESTED",
			"findings":               []map[string]any{{"id": "F1", "severity": "major", "summary": "bug", "evidence": "x.go:10"}},
			"recommendedDisposition": "correct and resubmit",
		},
	})
	resultSha := res["reviewResultSha256"].(string)
	if res["taskStatus"] != "reviewed-changes-requested" {
		t.Fatalf("verdict: %#v", res)
	}
	// Acceptance is impossible on a changes-requested review.
	wantErr(t, s, orch, "coordination.acceptance_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.final-acceptance.v0", "recordType": "final-acceptance",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T16:00:00Z", "createdByRole": "orchestrator",
			"headSha": testHeadSha, "handoffSha256": handoffSha, "reviewResultSha256": resultSha,
			"gates":            []map[string]any{},
			"artifactManifest": []map[string]any{{"path": "x", "sha256": strings.Repeat("e", 64)}},
		},
	}, "CONFLICT_MATERIAL_STATE")

	// The single correction revision publishes with finding references.
	correction := assignmentJSON(ws, taskID, 1, worktree)
	correction["correctionOf"] = map[string]any{
		"taskId": taskID, "revision": 0, "reviewResultSha256": resultSha, "findingIds": []string{"F1"},
	}
	pub := call(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": correction,
	})
	if pub["taskRevision"] != float64(1) {
		t.Fatalf("correction publish: %#v", pub)
	}
	// Unknown finding ids are refused.
	badCorrection := assignmentJSON(ws, "BUILD-001X", 1, worktree)
	badCorrection["taskId"] = "BUILD-001X"
	badCorrection["correctionOf"] = map[string]any{
		"taskId": "BUILD-001X", "revision": 0, "reviewResultSha256": resultSha, "findingIds": []string{"F9"},
	}
	if _, err := dispatch(s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": badCorrection,
	}); err == nil {
		t.Fatal("correction with unknown predecessor accepted")
	}
	// A second correction loop (revision 2) is out of scope by construction.
	overCorrection := assignmentJSON(ws, taskID, 2, worktree)
	overCorrection["correctionOf"] = map[string]any{
		"taskId": taskID, "revision": 1, "reviewResultSha256": resultSha, "findingIds": []string{"F1"},
	}
	wantErr(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": overCorrection,
	}, "INVALID_REQUEST")
}

// ---- durability, restart, artifacts --------------------------------------

func TestRestartPreservesStateAndIdempotency(t *testing.T) {
	root := t.TempDir()
	db := openTestDB(t, root)
	if _, err := db.Exec(`CREATE TABLE meta(k TEXT PRIMARY KEY,v TEXT NOT NULL);
CREATE TABLE workspaces(ref TEXT PRIMARY KEY,window_id TEXT UNIQUE,name TEXT,generation INTEGER,version INTEGER);
CREATE TABLE panes(ref TEXT PRIMARY KEY,workspace_ref TEXT,window_id TEXT,pane_id TEXT UNIQUE,generation INTEGER,version INTEGER,badge TEXT NOT NULL DEFAULT '',fenced INTEGER NOT NULL DEFAULT 0);
PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := "coord"
	setupWorkstream(t, s, ws)
	key := ikey()
	first := call(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": key,
		"record": assignmentJSON(ws, "BUILD-001", 0, "/tmp/wt"),
	})
	claimKey := ikey()
	call(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": claimKey,
		"taskId": "BUILD-001", "taskRevision": 0,
	})
	beforeStatus := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	// Plant a staged orphan to prove reconciliation prunes it.
	orphan := filepath.Join(root, "coord", "artifacts", "ff", ".stage-orphan")
	if err := os.MkdirAll(filepath.Dir(orphan), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("junk"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db2 := openTestDB(t, root)
	defer db2.Close()
	s2, err := New(db2, root, nil)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if _, err := os.Lstat(orphan); !os.IsNotExist(err) {
		t.Fatal("staged orphan survived reconciliation")
	}
	afterStatus := call(t, s2, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	bb, _ := json.Marshal(beforeStatus)
	ab, _ := json.Marshal(afterStatus)
	if string(bb) != string(ab) {
		t.Fatalf("restart changed projection:\n%s\n%s", bb, ab)
	}
	// Idempotent replay across restart returns the original result.
	replay := call(t, s2, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": 1, "idempotencyKey": key,
		"record": assignmentJSON(ws, "BUILD-001", 0, "/tmp/wt"),
	})
	if replay["replayed"] != true || replay["assignmentSha256"] != first["assignmentSha256"] {
		t.Fatalf("cross-restart replay: %#v", replay)
	}
	// The lease survives restart; a duplicate claim still fails.
	wantErr(t, s2, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s2, ws), "idempotencyKey": ikey(),
		"taskId": "BUILD-001", "taskRevision": 0,
	}, "CONFLICT_MATERIAL_STATE")
}

func TestArtifactTamperFailsClosed(t *testing.T) {
	s, _, root := newTestService(t, nil)
	ws := "coord"
	setupWorkstream(t, s, ws)
	sha := publishTask(t, s, ws, "BUILD-001", "/tmp/wt")
	path := filepath.Join(root, "coord", "artifacts", sha[:2], sha)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"recordType":"task-assignment","tampered":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch(s, orch, "coordination.artifact_read", map[string]any{"workstreamId": ws, "sha256": sha}); err == nil {
		t.Fatal("tampered artifact served")
	}
	// Reconciliation refuses to bring the controller up over corruption.
	if err := s.Reconcile(); err == nil {
		t.Fatal("reconcile accepted corrupt committed artifact")
	}
}

// ---- events, wait, bounds ------------------------------------------------

func TestEventsCursorAndBounds(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	ws := "coord"
	setupWorkstream(t, s, ws)
	for i := range 5 {
		publishTask(t, s, ws, fmt.Sprintf("T-%03d", i), "/tmp/wt")
	}
	page1 := call(t, s, orch, "coordination.events_list", map[string]any{"workstreamId": ws, "afterSeq": 0, "limit": 3})
	if len(page1["events"].([]any)) != 3 || page1["more"] != true {
		t.Fatalf("page1: %#v", page1)
	}
	next := int64(page1["nextAfterSeq"].(float64))
	page2 := call(t, s, orch, "coordination.events_list", map[string]any{"workstreamId": ws, "afterSeq": next, "limit": 200})
	if page2["more"] != false {
		t.Fatalf("page2: %#v", page2)
	}
	total := len(page1["events"].([]any)) + len(page2["events"].([]any))
	if total != 6 { // workstream.created + 5 publishes
		t.Fatalf("total events %d", total)
	}
	wantErr(t, s, orch, "coordination.events_list", map[string]any{"workstreamId": ws, "afterSeq": 0, "limit": 500}, "INVALID_REQUEST")
}

func TestWaitWakesAndDoesNotLeak(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	ws := "coord"
	setupWorkstream(t, s, ws)
	seq := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})["eventSeq"].(float64)
	done := make(chan map[string]any, 1)
	go func() {
		r, err := dispatch(s, orch, "coordination.wait", map[string]any{
			"workstreamId": ws, "afterSeq": seq,
			"deadline": time.Now().Add(5 * time.Second).UTC().Format(time.RFC3339),
		})
		if err != nil {
			done <- map[string]any{"err": err.Error()}
			return
		}
		done <- r
	}()
	time.Sleep(100 * time.Millisecond)
	publishTask(t, s, ws, "BUILD-001", "/tmp/wt")
	select {
	case r := <-done:
		if r["matched"] != true {
			t.Fatalf("wait: %#v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not wake on event")
	}
	if s.WaiterCount() != 0 {
		t.Fatalf("waiter leaked: %d", s.WaiterCount())
	}
	// A past cursor returns immediately; an expired deadline fails cleanly.
	r := call(t, s, orch, "coordination.wait", map[string]any{
		"workstreamId": ws, "afterSeq": 0,
		"deadline": time.Now().Add(time.Second).UTC().Format(time.RFC3339),
	})
	if r["matched"] != true {
		t.Fatalf("immediate wait: %#v", r)
	}
	wantErr(t, s, orch, "coordination.wait", map[string]any{
		"workstreamId": ws, "afterSeq": 10_000,
		"deadline": time.Now().Add(50 * time.Millisecond).UTC().Format(time.RFC3339),
	}, "DEADLINE_EXCEEDED")
	if s.WaiterCount() != 0 {
		t.Fatalf("waiter leaked after deadline: %d", s.WaiterCount())
	}
}

func TestCheckpointUsesDurableProjectionOnly(t *testing.T) {
	s, db, _ := newTestService(t, nil)
	ws := "coord"
	setupWorkstream(t, s, ws)
	if _, err := db.Exec("INSERT INTO workspaces(ref,window_id,name,generation,version) VALUES('cpw_x','@1','choir',1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO panes(ref,workspace_ref,window_id,pane_id,generation,version,badge) VALUES('cpp_a','cpw_x','@1','%1',1,4,'builder')"); err != nil {
		t.Fatal(err)
	}
	r := call(t, s, orch, "coordination.checkpoint_emit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"workspaceRef": "cpw_x", "reason": "rehearsal checkpoint",
	})
	if r["paneCount"] != float64(1) {
		t.Fatalf("checkpoint: %#v", r)
	}
	read := call(t, s, orch, "coordination.artifact_read", map[string]any{"workstreamId": ws, "sha256": r["checkpointSha256"].(string)})
	var cp WorkspaceCheckpoint
	rb, _ := json.Marshal(read["record"])
	if err := json.Unmarshal(rb, &cp); err != nil {
		t.Fatal(err)
	}
	if len(cp.Panes) != 1 || cp.Panes[0].PaneRef != "cpp_a" || cp.Panes[0].Version != 4 {
		t.Fatalf("checkpoint record: %#v", cp)
	}
	wantErr(t, s, orch, "coordination.checkpoint_emit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"workspaceRef": "cpw_missing", "reason": "x",
	}, "TARGET_NOT_FOUND")
}
