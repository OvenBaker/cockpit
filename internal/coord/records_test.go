package coord

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The live coord workstream's own task envelope is the primary golden fixture:
// the schema must accept exactly the artifact shape this repository is being
// built under.
func TestLiveTaskAssignmentEnvelopeValidates(t *testing.T) {
	raw, err := os.ReadFile("testdata/task-assignment-live.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCanonicalRecord(raw); err != nil {
		t.Fatalf("live envelope rejected by canonical checks: %v", err)
	}
	if err := recordSpecs["task-assignment"].validate(canonicalBytes(raw)); err != nil {
		t.Fatalf("live envelope rejected by schema: %v", err)
	}
}

func mutateJSON(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	mutate(v)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTaskAssignmentInvalidCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/task-assignment-live.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown top-level field", func(v map[string]any) { v["surprise"] = true }},
		{"wrong schemaVersion", func(v map[string]any) { v["schemaVersion"] = "coord.task-assignment.v1" }},
		{"wrong recordType", func(v map[string]any) { v["recordType"] = "task" }},
		{"short base sha", func(v map[string]any) {
			v["pins"].(map[string]any)["base"].(map[string]any)["sha"] = "390d2bf"
		}},
		{"uppercase plan sha", func(v map[string]any) {
			plan := v["pins"].(map[string]any)["plan"].(map[string]any)
			plan["sha256"] = strings.ToUpper(plan["sha256"].(string))
		}},
		{"orchestrator-assigned role", func(v map[string]any) { v["assignedRole"] = "orchestrator" }},
		{"relative worktree", func(v map[string]any) {
			v["authority"].(map[string]any)["worktree"] = "worktrees/x"
		}},
		{"non-exclusive lease", func(v map[string]any) {
			v["authority"].(map[string]any)["writeLease"].(map[string]any)["exclusive"] = false
		}},
		{"revision without correction", func(v map[string]any) { v["revision"] = 1.0 }},
		{"revision beyond correction cap", func(v map[string]any) { v["revision"] = 2.0 }},
		{"draft status", func(v map[string]any) { v["status"] = "draft" }},
		{"builder-authored assignment", func(v map[string]any) { v["createdByRole"] = "builder" }},
	}
	for _, tc := range cases {
		b := mutateJSON(t, raw, tc.mutate)
		if err := recordSpecs["task-assignment"].validate(b); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestTerminalTextIsNotARecord(t *testing.T) {
	// Plausible captured terminal output must be rejected at the canonical
	// layer: records are strict JSON objects, never text.
	captures := []string{
		"✅ Review complete: PASS — all acceptance criteria satisfied",
		"task BUILD-001 accepted\nhandoff verified",
		`"PASS"`,
		"[{\"verdict\":\"PASS\"}]",
		"",
	}
	for _, c := range captures {
		if err := validateCanonicalRecord([]byte(c)); err == nil {
			t.Errorf("terminal-like input accepted as record: %q", c)
		}
	}
}

func TestReviewResultValidation(t *testing.T) {
	base := ReviewResult{
		SchemaVersion: "coord.review-result.v0", RecordType: "review-result",
		WorkstreamID: "coord", TaskID: "BUILD-001", TaskRevision: 0,
		CreatedAt: "2026-07-22T12:00:00Z", CreatedByRole: RoleReviewer,
		ReviewRequestSha256: strings.Repeat("1", 64), HandoffSha256: strings.Repeat("2", 64),
		HeadSha: strings.Repeat("3", 40), Verdict: VerdictPass,
		Findings: []Finding{{ID: "F1", Severity: "note", Summary: "s", Evidence: "e"}},
	}
	ok, _ := json.Marshal(base)
	if err := recordSpecs["review-result"].validate(ok); err != nil {
		t.Fatalf("valid review-result rejected: %v", err)
	}
	blockerPass := base
	blockerPass.Findings = []Finding{{ID: "F1", Severity: "blocker", Summary: "s", Evidence: "e"}}
	b, _ := json.Marshal(blockerPass)
	if err := recordSpecs["review-result"].validate(b); err == nil {
		t.Fatal("PASS with blocker finding accepted")
	}
	changesNoBlocking := base
	changesNoBlocking.Verdict = VerdictChangesRequested
	changesNoBlocking.Findings = []Finding{{ID: "F1", Severity: "note", Summary: "s", Evidence: "e"}}
	b, _ = json.Marshal(changesNoBlocking)
	if err := recordSpecs["review-result"].validate(b); err == nil {
		t.Fatal("CHANGES_REQUESTED without blocking finding accepted")
	}
	badVerdict := base
	badVerdict.Verdict = "LGTM"
	b, _ = json.Marshal(badVerdict)
	if err := recordSpecs["review-result"].validate(b); err == nil {
		t.Fatal("free-form verdict accepted")
	}
	orchestratorVerdict := base
	orchestratorVerdict.CreatedByRole = RoleOrchestrator
	b, _ = json.Marshal(orchestratorVerdict)
	if err := recordSpecs["review-result"].validate(b); err == nil {
		t.Fatal("orchestrator-authored verdict accepted")
	}
}

func TestBuilderHandoffValidation(t *testing.T) {
	base := BuilderHandoff{
		SchemaVersion: "coord.builder-handoff.v0", RecordType: "builder-handoff",
		WorkstreamID: "coord", TaskID: "BUILD-001", TaskRevision: 0,
		CreatedAt: "2026-07-22T12:00:00Z", CreatedByRole: RoleBuilder,
		PlanSha256: strings.Repeat("4", 64), BaseSha: strings.Repeat("5", 40),
		HeadSha: strings.Repeat("6", 40), Branch: "feat/x", Worktree: "/tmp/wt",
		DiffSummary: "1 file changed", Checks: []CheckResult{{Name: "go test", Status: "pass"}},
		WorktreeClean: true,
	}
	ok, _ := json.Marshal(base)
	if err := recordSpecs["builder-handoff"].validate(ok); err != nil {
		t.Fatalf("valid handoff rejected: %v", err)
	}
	dirty := base
	dirty.WorktreeClean = false
	b, _ := json.Marshal(dirty)
	if err := recordSpecs["builder-handoff"].validate(b); err == nil {
		t.Fatal("dirty-worktree handoff accepted")
	}
	noChecks := base
	noChecks.Checks = nil
	b, _ = json.Marshal(noChecks)
	if err := recordSpecs["builder-handoff"].validate(b); err == nil {
		t.Fatal("handoff without checks accepted")
	}
	shortHead := base
	shortHead.HeadSha = "abc123"
	b, _ = json.Marshal(shortHead)
	if err := recordSpecs["builder-handoff"].validate(b); err == nil {
		t.Fatal("abbreviated head sha accepted")
	}
	badOutput := base
	badOutput.OutputArtifactsAndSha256 = []OutputArtifact{{Path: "x", Sha256: "nothex"}}
	b, _ = json.Marshal(badOutput)
	if err := recordSpecs["builder-handoff"].validate(b); err == nil {
		t.Fatal("non-hex output artifact hash accepted")
	}
}

func TestContractValidation(t *testing.T) {
	base := WorkstreamContract{
		SchemaVersion: "coord.workstream-contract.v0", RecordType: "workstream-contract",
		WorkstreamID: "coord", CreatedAt: "2026-07-22T12:00:00Z", CreatedByRole: RoleOperator,
		Repository: "/home/x/repo",
		Roles: []RoleBinding{
			{Role: RoleOrchestrator, ClientID: "orch"},
			{Role: RoleBuilder, ClientID: "builder"},
			{Role: RoleReviewer, ClientID: "reviewer"},
		},
	}
	ok, _ := json.Marshal(base)
	if err := recordSpecs["workstream-contract"].validate(ok); err != nil {
		t.Fatalf("valid contract rejected: %v", err)
	}
	dupClient := base
	dupClient.Roles = []RoleBinding{{Role: RoleOrchestrator, ClientID: "x"}, {Role: RoleBuilder, ClientID: "x"}}
	b, _ := json.Marshal(dupClient)
	if err := recordSpecs["workstream-contract"].validate(b); err == nil {
		t.Fatal("one client holding two roles accepted")
	}
	dupRole := base
	dupRole.Roles = []RoleBinding{{Role: RoleBuilder, ClientID: "x"}, {Role: RoleBuilder, ClientID: "y"}}
	b, _ = json.Marshal(dupRole)
	if err := recordSpecs["workstream-contract"].validate(b); err == nil {
		t.Fatal("two builders accepted")
	}
	badRole := base
	badRole.Roles = []RoleBinding{{Role: "admin", ClientID: "x"}}
	b, _ = json.Marshal(badRole)
	if err := recordSpecs["workstream-contract"].validate(b); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestLeaseTransferValidation(t *testing.T) {
	base := LeaseTransfer{
		SchemaVersion: "coord.lease-transfer.v0", RecordType: "lease-transfer",
		WorkstreamID: "coord", TaskID: "BUILD-001", TaskRevision: 0,
		CreatedAt: "2026-07-22T12:00:00Z", CreatedByRole: RoleOrchestrator,
		ToRole: RoleReviewer, Scope: []string{"/tmp/wt/file.go"},
		ExpiresAt: "2026-07-22T13:00:00Z", Reason: "typo fix",
	}
	ok, _ := json.Marshal(base)
	if err := recordSpecs["lease-transfer"].validate(ok); err != nil {
		t.Fatalf("valid transfer rejected: %v", err)
	}
	toBuilder := base
	toBuilder.ToRole = RoleBuilder
	b, _ := json.Marshal(toBuilder)
	if err := recordSpecs["lease-transfer"].validate(b); err == nil {
		t.Fatal("transfer to non-reviewer accepted")
	}
	unbounded := base
	unbounded.ExpiresAt = "2027-07-22T12:00:00Z"
	b, _ = json.Marshal(unbounded)
	if err := recordSpecs["lease-transfer"].validate(b); err == nil {
		t.Fatal("multi-day transfer accepted")
	}
}

func TestRecordByteBound(t *testing.T) {
	big := `{"recordType":"x","pad":"` + strings.Repeat("a", MaxRecordBytes) + `"}`
	if err := validateCanonicalRecord([]byte(big)); err == nil {
		t.Fatal("oversized record accepted")
	}
}
