package coord

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBriefSha = "17679f6a0228b4cf3ecd67a07307704db44ec783f7acb02945443444b6d88f06"

func policyBinding() map[string]any {
	return map[string]any{"version": "policy-v1", "briefPackageSha256": testBriefSha}
}

// The five policy-v1 assets in docs/policy-v1 must remain byte-for-byte
// identical to the digests frozen in MANIFEST.json, and the manifest must pin
// the brief-package hash. This makes the repository copy tamper-evident.
func TestPolicyAssetsMatchManifest(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "docs", "policy-v1", "MANIFEST.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SchemaVersion          string `json:"schemaVersion"`
		PackageVersion         string `json:"packageVersion"`
		BriefPackageSha256     string `json:"briefPackageSha256"`
		BriefPackageSourcePath string `json:"briefPackageSourcePath"`
		Disposition            string `json:"disposition"`
		Assets                 []struct {
			Role       string `json:"role"`
			Path       string `json:"path"`
			SourcePath string `json:"sourcePath"`
			Sha256     string `json:"sha256"`
		} `json:"assets"`
	}
	if err := strictDecode(raw, &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.PackageVersion != "policy-v1" || !sha256RE.MatchString(manifest.BriefPackageSha256) {
		t.Fatalf("manifest header: %+v", manifest)
	}
	if len(manifest.Assets) != 5 {
		t.Fatalf("expected exactly five policy assets, got %d", len(manifest.Assets))
	}
	roles := map[string]bool{}
	for _, a := range manifest.Assets {
		if roles[a.Role] {
			t.Fatalf("duplicate asset role %s", a.Role)
		}
		roles[a.Role] = true
		b, err := os.ReadFile(filepath.Join(root, a.Path))
		if err != nil {
			t.Fatalf("%s: %v", a.Path, err)
		}
		if got := sha256Hex(b); got != a.Sha256 {
			t.Errorf("%s drifted from frozen policy input: got %s want %s", a.Path, got, a.Sha256)
		}
	}
	for _, want := range []string{"operating-policy", "orchestrator-guiding-prompt", "builder-guiding-prompt", "reviewer-guiding-prompt", "release-conductor-guiding-prompt"} {
		if !roles[want] {
			t.Errorf("manifest missing role %s", want)
		}
	}
}

func TestPolicyBindingValidation(t *testing.T) {
	ok := PolicyBinding{Version: "policy-v1", BriefPackageSha256: testBriefSha}
	if err := (&ok).validate(); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	if err := (&PolicyBinding{Version: "", BriefPackageSha256: testBriefSha}).validate(); err == nil {
		t.Fatal("empty version accepted")
	}
	if err := (&PolicyBinding{Version: "Policy V1!", BriefPackageSha256: testBriefSha}).validate(); err == nil {
		t.Fatal("unbounded version accepted")
	}
	if err := (&PolicyBinding{Version: "policy-v1", BriefPackageSha256: "nothex"}).validate(); err == nil {
		t.Fatal("non-hex brief hash accepted")
	}
	var nilBinding *PolicyBinding
	if err := nilBinding.validate(); err != nil {
		t.Fatal("absent binding must validate")
	}
}

func TestFindingClassCorpus(t *testing.T) {
	base := ReviewResult{
		SchemaVersion: "coord.review-result.v0", RecordType: "review-result",
		WorkstreamID: "coord", TaskID: "BUILD-001", TaskRevision: 0,
		CreatedAt: "2026-07-22T12:00:00Z", CreatedByRole: RoleReviewer,
		ReviewRequestSha256: strings.Repeat("1", 64), HandoffSha256: strings.Repeat("2", 64),
		HeadSha: strings.Repeat("3", 40), Verdict: VerdictPass,
	}
	valid := func(r ReviewResult) error {
		b, _ := json.Marshal(r)
		return recordSpecs["review-result"].validate(b)
	}
	// Classified findings with consistent severities pass.
	r := base
	r.Policy = &PolicyBinding{Version: "policy-v1", BriefPackageSha256: testBriefSha}
	r.Findings = []Finding{
		{ID: "F1", Severity: "note", Class: "valid-follow-up", Summary: "s", Evidence: "e"},
		{ID: "F2", Severity: "minor", Class: "irrelevant-nitpick", Summary: "s", Evidence: "e"},
		{ID: "F3", Severity: "note", Class: "reviewer-error", Summary: "s", Evidence: "e"},
	}
	if err := valid(r); err != nil {
		t.Fatalf("classified pass rejected: %v", err)
	}
	// A policy-bound review must classify every finding.
	r.Findings = []Finding{{ID: "F1", Severity: "note", Summary: "s", Evidence: "e"}}
	if err := valid(r); err == nil {
		t.Fatal("policy-bound unclassified finding accepted")
	}
	// Unknown class is refused.
	r.Findings = []Finding{{ID: "F1", Severity: "note", Class: "wontfix", Summary: "s", Evidence: "e"}}
	if err := valid(r); err == nil {
		t.Fatal("unknown class accepted")
	}
	// Class/severity inconsistency is refused: a nitpick can never block.
	r.Verdict = VerdictChangesRequested
	r.Findings = []Finding{{ID: "F1", Severity: "blocker", Class: "irrelevant-nitpick", Summary: "s", Evidence: "e"}}
	if err := valid(r); err == nil {
		t.Fatal("blocking nitpick accepted")
	}
	// An in-scope blocker must carry blocker severity.
	r.Findings = []Finding{{ID: "F1", Severity: "minor", Class: "in-scope-blocker", Summary: "s", Evidence: "e"}}
	if err := valid(r); err == nil {
		t.Fatal("minor in-scope-blocker accepted")
	}
	// Correct blocking triage passes.
	r.Findings = []Finding{{ID: "F1", Severity: "blocker", Class: "in-scope-blocker", Summary: "s", Evidence: "e"}}
	if err := valid(r); err != nil {
		t.Fatalf("classified blocker rejected: %v", err)
	}
	// Without a policy binding, classification stays optional but validated.
	u := base
	u.Findings = []Finding{{ID: "F1", Severity: "note", Summary: "s", Evidence: "e"}}
	if err := valid(u); err != nil {
		t.Fatalf("unbound unclassified finding rejected: %v", err)
	}
}

// The policy binding originates in the task assignment and must be carried
// exactly by every downstream lifecycle record; an unbound task refuses
// bound downstream records.
func TestPolicyBindingEnforcementAcrossLifecycle(t *testing.T) {
	s, db, _ := newTestService(t, nil)
	ws, taskID, worktree := "coord", "BUILD-001", "/tmp/wt"
	setupWorkstream(t, s, ws)

	// Publish a policy-bound assignment.
	bound := assignmentJSON(ws, taskID, 0, worktree)
	bound["policy"] = policyBinding()
	call(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": bound,
	})
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	task := st["tasks"].([]any)[0].(map[string]any)
	if task["policyVersion"] != "policy-v1" || task["briefPackageSha256"] != testBriefSha {
		t.Fatalf("status lacks policy binding: %#v", task)
	}
	call(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "taskId": taskID, "taskRevision": 0,
	})

	// A handoff without the binding, or with a drifted hash, is refused
	// without mutation.
	before := snapshot(t, db)
	unbound := handoffJSON(ws, taskID, 0, worktree)
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": unbound,
	}, "CONFLICT_MATERIAL_STATE")
	drifted := handoffJSON(ws, taskID, 0, worktree)
	drifted["policy"] = map[string]any{"version": "policy-v1", "briefPackageSha256": strings.Repeat("9", 64)}
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": drifted,
	}, "CONFLICT_MATERIAL_STATE")
	wrongVersion := handoffJSON(ws, taskID, 0, worktree)
	wrongVersion["policy"] = map[string]any{"version": "policy-v2", "briefPackageSha256": testBriefSha}
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": wrongVersion,
	}, "CONFLICT_MATERIAL_STATE")
	if after := snapshot(t, db); after != before {
		t.Fatal("refused policy-unbound handoffs mutated state")
	}

	// The exact binding rides the whole lifecycle.
	handoff := handoffJSON(ws, taskID, 0, worktree)
	handoff["policy"] = policyBinding()
	ho := call(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": handoff,
	})
	handoffSha := ho["handoffSha256"].(string)
	reviewReq := map[string]any{
		"schemaVersion": "coord.review-request.v0", "recordType": "review-request",
		"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
		"createdAt": "2026-07-22T14:00:00Z", "createdByRole": "orchestrator",
		"handoffSha256": handoffSha, "headSha": testHeadSha, "baseSha": testBaseSha,
		"reviewScope": []string{}, "policy": policyBinding(),
	}
	call(t, s, orch, "coordination.review_request", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": reviewReq,
	})
	st = call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	requestSha := st["tasks"].([]any)[0].(map[string]any)["reviewRequestSha256"].(string)
	// A policy-bound verdict must classify findings and carry the binding.
	verdict := map[string]any{
		"schemaVersion": "coord.review-result.v0", "recordType": "review-result",
		"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
		"createdAt": "2026-07-22T15:00:00Z", "createdByRole": "reviewer",
		"reviewRequestSha256": requestSha, "handoffSha256": handoffSha, "headSha": testHeadSha,
		"verdict":                "PASS_WITH_NONBLOCKING_NOTES",
		"findings":               []map[string]any{{"id": "F1", "severity": "note", "class": "valid-follow-up", "summary": "s", "evidence": "e"}},
		"recommendedDisposition": "accept", "policy": policyBinding(),
	}
	rs := call(t, s, reviewer, "coordination.review_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": verdict,
	})
	resultSha := rs["reviewResultSha256"].(string)
	acceptance := map[string]any{
		"schemaVersion": "coord.final-acceptance.v0", "recordType": "final-acceptance",
		"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
		"createdAt": "2026-07-22T16:00:00Z", "createdByRole": "orchestrator",
		"headSha": testHeadSha, "handoffSha256": handoffSha, "reviewResultSha256": resultSha,
		"gates":            []map[string]any{{"name": "focused", "status": "pass", "detail": ""}},
		"artifactManifest": []map[string]any{{"path": "x", "sha256": strings.Repeat("e", 64)}},
		"policy":           policyBinding(),
	}
	acc := call(t, s, orch, "coordination.acceptance_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": acceptance,
	})
	// A release handoff that sheds the binding is refused.
	release := map[string]any{
		"schemaVersion": "coord.release-handoff.v0", "recordType": "release-handoff",
		"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
		"createdAt": "2026-07-22T17:00:00Z", "createdByRole": "orchestrator",
		"acceptanceSha256": acc["acceptanceSha256"].(string), "headSha": testHeadSha, "baseSha": testBaseSha,
		"branch": "feat/test", "notes": []string{},
	}
	wantErr(t, s, orch, "coordination.release_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": release,
	}, "CONFLICT_MATERIAL_STATE")
	release["policy"] = policyBinding()
	rel := call(t, s, orch, "coordination.release_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": release,
	})
	if rel["taskStatus"] != "released" {
		t.Fatalf("release: %#v", rel)
	}

	// An unbound task refuses a bound handoff: binding cannot be introduced
	// mid-stream.
	publishTask(t, s, ws, "BUILD-002", "/tmp/wt2")
	call(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "taskId": "BUILD-002", "taskRevision": 0,
	})
	boundHandoff := handoffJSON(ws, "BUILD-002", 0, "/tmp/wt2")
	boundHandoff["worktree"] = "/tmp/wt2"
	boundHandoff["policy"] = policyBinding()
	wantErr(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": boundHandoff,
	}, "CONFLICT_MATERIAL_STATE")
}

// A correction of a policy-bound task must preserve the identical binding.
func TestCorrectionPreservesPolicyBinding(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	ws, taskID, worktree := "coord", "BUILD-001", "/tmp/wt"
	setupWorkstream(t, s, ws)
	bound := assignmentJSON(ws, taskID, 0, worktree)
	bound["policy"] = policyBinding()
	call(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": bound,
	})
	call(t, s, builder, "coordination.task_claim", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "taskId": taskID, "taskRevision": 0,
	})
	handoff := handoffJSON(ws, taskID, 0, worktree)
	handoff["policy"] = policyBinding()
	ho := call(t, s, builder, "coordination.handoff_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": handoff,
	})
	handoffSha := ho["handoffSha256"].(string)
	call(t, s, orch, "coordination.review_request", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-request.v0", "recordType": "review-request",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T14:00:00Z", "createdByRole": "orchestrator",
			"handoffSha256": handoffSha, "headSha": testHeadSha, "baseSha": testBaseSha,
			"reviewScope": []string{}, "policy": policyBinding(),
		},
	})
	st := call(t, s, orch, "coordination.status_get", map[string]any{"workstreamId": ws})
	requestSha := st["tasks"].([]any)[0].(map[string]any)["reviewRequestSha256"].(string)
	rs := call(t, s, reviewer, "coordination.review_submit", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(),
		"record": map[string]any{
			"schemaVersion": "coord.review-result.v0", "recordType": "review-result",
			"workstreamId": ws, "taskId": taskID, "taskRevision": 0,
			"createdAt": "2026-07-22T15:00:00Z", "createdByRole": "reviewer",
			"reviewRequestSha256": requestSha, "handoffSha256": handoffSha, "headSha": testHeadSha,
			"verdict":                "CHANGES_REQUESTED",
			"findings":               []map[string]any{{"id": "F1", "severity": "major", "class": "in-scope-material", "summary": "bug", "evidence": "x.go:1"}},
			"recommendedDisposition": "correct", "policy": policyBinding(),
		},
	})
	resultSha := rs["reviewResultSha256"].(string)
	// A correction that drops the binding is refused.
	correction := assignmentJSON(ws, taskID, 1, worktree)
	correction["correctionOf"] = map[string]any{"taskId": taskID, "revision": 0, "reviewResultSha256": resultSha, "findingIds": []string{"F1"}}
	wantErr(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": correction,
	}, "CONFLICT_MATERIAL_STATE")
	correction["policy"] = policyBinding()
	pub := call(t, s, orch, "coordination.task_publish", map[string]any{
		"workstreamId": ws, "expectedRevision": rev(t, s, ws), "idempotencyKey": ikey(), "record": correction,
	})
	if pub["taskRevision"] != float64(1) {
		t.Fatalf("bound correction: %#v", pub)
	}
}
