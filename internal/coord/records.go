package coord

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Every stored record is a strict, versioned JSON document. Caller-authored
// records are decoded with unknown-field rejection and validated before any
// mutation; controller-authored records are marshaled from these same structs.
// SHA-256 is always computed over the exact stored canonical bytes.

// ---- shared sub-records -------------------------------------------------

type Criterion struct {
	ID        string `json:"id"`
	Criterion string `json:"criterion"`
}

type Assignee struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	ApprovalMode string `json:"approvalMode"`
}

type WriteLeaseSpec struct {
	LeaseID                            string `json:"leaseId"`
	Scope                              string `json:"scope"`
	HolderRole                         string `json:"holderRole"`
	Exclusive                          bool   `json:"exclusive"`
	AcquireOnStructuredAcknowledgement bool   `json:"acquireOnStructuredAcknowledgement"`
	ReleaseOnValidHandoff              bool   `json:"releaseOnValidHandoff"`
}

type Authority struct {
	Repository          string         `json:"repository"`
	RepositoryAuthority string         `json:"repositoryAuthority"`
	Worktree            string         `json:"worktree"`
	Branch              string         `json:"branch"`
	AllowedWrites       []string       `json:"allowedWrites"`
	ForbiddenWrites     []string       `json:"forbiddenWrites"`
	May                 []string       `json:"may"`
	MayNot              []string       `json:"mayNot"`
	WriteLease          WriteLeaseSpec `json:"writeLease"`
}

type PlanPin struct {
	Path     string `json:"path"`
	Sha256   string `json:"sha256"`
	Revision int64  `json:"revision"`
}

type FilePin struct {
	Path   string `json:"path"`
	Sha256 string `json:"sha256"`
}

type BasePin struct {
	Ref         string `json:"ref"`
	RefreshedAt string `json:"refreshedAt"`
	Sha         string `json:"sha"`
}

type SeededPin struct {
	Ref               string   `json:"ref"`
	Sha               string   `json:"sha"`
	Interface         []string `json:"interface"`
	IntegrationPolicy string   `json:"integrationPolicy"`
}

type Pins struct {
	Plan                  PlanPin    `json:"plan"`
	OwnerReset            *FilePin   `json:"ownerReset,omitempty"`
	Base                  BasePin    `json:"base"`
	SeededInputCapability *SeededPin `json:"seededInputCapability,omitempty"`
}

type ScopeSpec struct {
	Required             []string `json:"required"`
	ExplicitlyOutOfScope []string `json:"explicitlyOutOfScope"`
}

type HandoffSpec struct {
	Delivery       string   `json:"delivery"`
	RequiredFields []string `json:"requiredFields"`
}

type RequiredOutputs struct {
	Repository []string    `json:"repository"`
	Handoff    HandoffSpec `json:"handoff"`
}

type CorrectionOf struct {
	TaskID             string   `json:"taskId"`
	Revision           int64    `json:"revision"`
	ReviewResultSha256 string   `json:"reviewResultSha256"`
	FindingIDs         []string `json:"findingIds"`
}

type CheckResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | fail | skipped
	Detail string `json:"detail"`
}

type OutputArtifact struct {
	Path   string `json:"path"`
	Sha256 string `json:"sha256"`
}

type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // blocker | major | minor | note
	Summary  string `json:"summary"`
	Evidence string `json:"evidence"`
}

type RoleBinding struct {
	Role     string `json:"role"`
	ClientID string `json:"clientId"`
}

// ---- caller-authored records --------------------------------------------

// TaskAssignment is the orchestrator-published task envelope
// (coord.task-assignment.v0). Its shape matches the live envelopes already
// used by the coord workstream so the real published artifacts validate.
type TaskAssignment struct {
	SchemaVersion          string          `json:"schemaVersion"`
	RecordType             string          `json:"recordType"`
	WorkstreamID           string          `json:"workstreamId"`
	TaskID                 string          `json:"taskId"`
	Revision               int64           `json:"revision"`
	Status                 string          `json:"status"`
	CreatedAt              string          `json:"createdAt"`
	CreatedByRole          string          `json:"createdByRole"`
	AssignedRole           string          `json:"assignedRole"`
	Assignee               Assignee        `json:"assignee"`
	Authority              Authority       `json:"authority"`
	Pins                   Pins            `json:"pins"`
	Objective              string          `json:"objective"`
	Scope                  ScopeSpec       `json:"scope"`
	ImplementationGuidance []string        `json:"implementationGuidance"`
	AcceptanceCriteria     []Criterion     `json:"acceptanceCriteria"`
	FocusedVerification    []string        `json:"focusedVerification"`
	RequiredOutputs        RequiredOutputs `json:"requiredOutputs"`
	StopConditions         []string        `json:"stopConditions"`
	CorrectionOf           *CorrectionOf   `json:"correctionOf,omitempty"`
}

func (r *TaskAssignment) validate() error {
	if r.SchemaVersion != "coord.task-assignment.v0" || r.RecordType != "task-assignment" {
		return cerr("INVALID_REQUEST", "task-assignment schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) {
		return cerr("INVALID_REQUEST", "invalid workstreamId")
	}
	if !taskIDRE.MatchString(r.TaskID) {
		return cerr("INVALID_REQUEST", "invalid taskId")
	}
	if r.Revision < 0 || r.Revision > maxCorrections {
		return cerr("INVALID_REQUEST", fmt.Sprintf("task revision must be 0..%d", maxCorrections))
	}
	if r.Status != StatusPublished {
		return cerr("INVALID_REQUEST", "task-assignment status must be published")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleOrchestrator {
		return cerr("INVALID_REQUEST", "task-assignment createdByRole must be orchestrator")
	}
	if r.AssignedRole != RoleBuilder {
		return cerr("INVALID_REQUEST", "task-assignment assignedRole must be builder")
	}
	if !sha256RE.MatchString(r.Pins.Plan.Sha256) {
		return cerr("INVALID_REQUEST", "pins.plan.sha256 must be 64 lowercase hex")
	}
	if !gitShaRE.MatchString(r.Pins.Base.Sha) {
		return cerr("INVALID_REQUEST", "pins.base.sha must be a full 40-hex commit SHA")
	}
	if r.Pins.SeededInputCapability != nil && !gitShaRE.MatchString(r.Pins.SeededInputCapability.Sha) {
		return cerr("INVALID_REQUEST", "pins.seededInputCapability.sha must be a full 40-hex commit SHA")
	}
	if !filepath.IsAbs(r.Authority.Worktree) {
		return cerr("INVALID_REQUEST", "authority.worktree must be absolute")
	}
	if r.Authority.Branch == "" || len(r.Authority.Branch) > maxShortString {
		return cerr("INVALID_REQUEST", "authority.branch is required and bounded")
	}
	if !leaseIDRE.MatchString(r.Authority.WriteLease.LeaseID) {
		return cerr("INVALID_REQUEST", "authority.writeLease.leaseId is required")
	}
	if !r.Authority.WriteLease.Exclusive || r.Authority.WriteLease.HolderRole != RoleBuilder {
		return cerr("INVALID_REQUEST", "writeLease must be exclusive and builder-held")
	}
	if r.Authority.WriteLease.Scope == "" || !filepath.IsAbs(r.Authority.WriteLease.Scope) {
		return cerr("INVALID_REQUEST", "writeLease.scope must be absolute")
	}
	for _, l := range [][]string{r.Authority.AllowedWrites, r.Authority.ForbiddenWrites, r.Authority.May, r.Authority.MayNot, r.ImplementationGuidance, r.FocusedVerification, r.StopConditions, r.Scope.Required, r.Scope.ExplicitlyOutOfScope, r.RequiredOutputs.Repository, r.RequiredOutputs.Handoff.RequiredFields} {
		if err := boundedList(len(l), "task-assignment list"); err != nil {
			return err
		}
		for _, s := range l {
			if err := boundedString(s, maxLongString, "task-assignment list item"); err != nil {
				return err
			}
		}
	}
	if err := boundedString(r.Objective, maxLongString, "objective"); err != nil {
		return err
	}
	if err := boundedList(len(r.AcceptanceCriteria), "acceptanceCriteria"); err != nil {
		return err
	}
	for _, c := range r.AcceptanceCriteria {
		if c.ID == "" || len(c.ID) > maxShortString {
			return cerr("INVALID_REQUEST", "acceptance criterion id is required and bounded")
		}
		if err := boundedString(c.Criterion, maxLongString, "acceptance criterion"); err != nil {
			return err
		}
	}
	if r.CorrectionOf != nil {
		if !taskIDRE.MatchString(r.CorrectionOf.TaskID) || r.CorrectionOf.TaskID != r.TaskID {
			return cerr("INVALID_REQUEST", "correctionOf.taskId must match taskId")
		}
		if r.CorrectionOf.Revision != r.Revision-1 {
			return cerr("INVALID_REQUEST", "correctionOf.revision must be the immediately prior revision")
		}
		if !sha256RE.MatchString(r.CorrectionOf.ReviewResultSha256) {
			return cerr("INVALID_REQUEST", "correctionOf.reviewResultSha256 must be 64 lowercase hex")
		}
		if len(r.CorrectionOf.FindingIDs) == 0 {
			return cerr("INVALID_REQUEST", "correctionOf.findingIds must reference at least one finding")
		}
		if err := boundedList(len(r.CorrectionOf.FindingIDs), "correctionOf.findingIds"); err != nil {
			return err
		}
	} else if r.Revision != 0 {
		return cerr("INVALID_REQUEST", "revision above 0 requires correctionOf")
	}
	return nil
}

// BuilderHandoff (coord.builder-handoff.v0) binds a committed exact head SHA,
// clean worktree, checks, and declared output hashes.
type BuilderHandoff struct {
	SchemaVersion            string           `json:"schemaVersion"`
	RecordType               string           `json:"recordType"`
	WorkstreamID             string           `json:"workstreamId"`
	TaskID                   string           `json:"taskId"`
	TaskRevision             int64            `json:"taskRevision"`
	CreatedAt                string           `json:"createdAt"`
	CreatedByRole            string           `json:"createdByRole"`
	PlanSha256               string           `json:"planSha256"`
	BaseSha                  string           `json:"baseSha"`
	HeadSha                  string           `json:"headSha"`
	Branch                   string           `json:"branch"`
	Worktree                 string           `json:"worktree"`
	CommitSummary            []string         `json:"commitSummary"`
	DiffSummary              string           `json:"diffSummary"`
	Checks                   []CheckResult    `json:"checks"`
	KnownLimitations         []string         `json:"knownLimitations"`
	OutputArtifactsAndSha256 []OutputArtifact `json:"outputArtifactsAndSha256"`
	WorktreeClean            bool             `json:"worktreeClean"`
	SeededInputDependencySha string           `json:"seededInputDependencySha"`
}

func (r *BuilderHandoff) validate() error {
	if r.SchemaVersion != "coord.builder-handoff.v0" || r.RecordType != "builder-handoff" {
		return cerr("INVALID_REQUEST", "builder-handoff schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) || !taskIDRE.MatchString(r.TaskID) || r.TaskRevision < 0 {
		return cerr("INVALID_REQUEST", "invalid builder-handoff identity")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleBuilder {
		return cerr("INVALID_REQUEST", "builder-handoff createdByRole must be builder")
	}
	if !sha256RE.MatchString(r.PlanSha256) {
		return cerr("INVALID_REQUEST", "planSha256 must be 64 lowercase hex")
	}
	if !gitShaRE.MatchString(r.BaseSha) || !gitShaRE.MatchString(r.HeadSha) {
		return cerr("INVALID_REQUEST", "baseSha and headSha must be full 40-hex commit SHAs")
	}
	if r.SeededInputDependencySha != "" && !gitShaRE.MatchString(r.SeededInputDependencySha) {
		return cerr("INVALID_REQUEST", "seededInputDependencySha must be a full 40-hex commit SHA")
	}
	if r.Branch == "" || len(r.Branch) > maxShortString || !filepath.IsAbs(r.Worktree) {
		return cerr("INVALID_REQUEST", "branch and absolute worktree are required")
	}
	if !r.WorktreeClean {
		return cerr("INVALID_REQUEST", "a builder handoff requires a clean worktree")
	}
	if err := boundedList(len(r.CommitSummary), "commitSummary"); err != nil {
		return err
	}
	for _, s := range r.CommitSummary {
		if err := boundedString(s, maxLongString, "commitSummary item"); err != nil {
			return err
		}
	}
	if err := boundedString(r.DiffSummary, maxTextString, "diffSummary"); err != nil {
		return err
	}
	if len(r.Checks) == 0 {
		return cerr("INVALID_REQUEST", "at least one focused check is required")
	}
	if err := boundedList(len(r.Checks), "checks"); err != nil {
		return err
	}
	for _, c := range r.Checks {
		if c.Name == "" || len(c.Name) > maxShortString {
			return cerr("INVALID_REQUEST", "check name is required and bounded")
		}
		if c.Status != "pass" && c.Status != "fail" && c.Status != "skipped" {
			return cerr("INVALID_REQUEST", "check status must be pass|fail|skipped")
		}
		if err := boundedString(c.Detail, maxLongString, "check detail"); err != nil {
			return err
		}
	}
	if err := boundedList(len(r.KnownLimitations), "knownLimitations"); err != nil {
		return err
	}
	for _, s := range r.KnownLimitations {
		if err := boundedString(s, maxLongString, "knownLimitations item"); err != nil {
			return err
		}
	}
	if err := boundedList(len(r.OutputArtifactsAndSha256), "outputArtifactsAndSha256"); err != nil {
		return err
	}
	for _, a := range r.OutputArtifactsAndSha256 {
		if a.Path == "" || len(a.Path) > maxLongString {
			return cerr("INVALID_REQUEST", "output artifact path is required and bounded")
		}
		if !sha256RE.MatchString(a.Sha256) {
			return cerr("INVALID_REQUEST", "output artifact sha256 must be 64 lowercase hex")
		}
	}
	return nil
}

// ReviewRequest (coord.review-request.v0) pins one builder handoff and its
// exact head SHA.
type ReviewRequest struct {
	SchemaVersion string   `json:"schemaVersion"`
	RecordType    string   `json:"recordType"`
	WorkstreamID  string   `json:"workstreamId"`
	TaskID        string   `json:"taskId"`
	TaskRevision  int64    `json:"taskRevision"`
	CreatedAt     string   `json:"createdAt"`
	CreatedByRole string   `json:"createdByRole"`
	HandoffSha256 string   `json:"handoffSha256"`
	HeadSha       string   `json:"headSha"`
	BaseSha       string   `json:"baseSha"`
	ReviewScope   []string `json:"reviewScope"`
}

func (r *ReviewRequest) validate() error {
	if r.SchemaVersion != "coord.review-request.v0" || r.RecordType != "review-request" {
		return cerr("INVALID_REQUEST", "review-request schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) || !taskIDRE.MatchString(r.TaskID) || r.TaskRevision < 0 {
		return cerr("INVALID_REQUEST", "invalid review-request identity")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleOrchestrator {
		return cerr("INVALID_REQUEST", "review-request createdByRole must be orchestrator")
	}
	if !sha256RE.MatchString(r.HandoffSha256) {
		return cerr("INVALID_REQUEST", "handoffSha256 must be 64 lowercase hex")
	}
	if !gitShaRE.MatchString(r.HeadSha) || !gitShaRE.MatchString(r.BaseSha) {
		return cerr("INVALID_REQUEST", "headSha and baseSha must be full 40-hex commit SHAs")
	}
	if err := boundedList(len(r.ReviewScope), "reviewScope"); err != nil {
		return err
	}
	for _, s := range r.ReviewScope {
		if err := boundedString(s, maxLongString, "reviewScope item"); err != nil {
			return err
		}
	}
	return nil
}

// ReviewResult (coord.review-result.v0) is the single immutable reviewer
// verdict for one exact head SHA.
type ReviewResult struct {
	SchemaVersion          string    `json:"schemaVersion"`
	RecordType             string    `json:"recordType"`
	WorkstreamID           string    `json:"workstreamId"`
	TaskID                 string    `json:"taskId"`
	TaskRevision           int64     `json:"taskRevision"`
	CreatedAt              string    `json:"createdAt"`
	CreatedByRole          string    `json:"createdByRole"`
	ReviewRequestSha256    string    `json:"reviewRequestSha256"`
	HandoffSha256          string    `json:"handoffSha256"`
	HeadSha                string    `json:"headSha"`
	Verdict                string    `json:"verdict"`
	Findings               []Finding `json:"findings"`
	RecommendedDisposition string    `json:"recommendedDisposition"`
}

func (r *ReviewResult) validate() error {
	if r.SchemaVersion != "coord.review-result.v0" || r.RecordType != "review-result" {
		return cerr("INVALID_REQUEST", "review-result schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) || !taskIDRE.MatchString(r.TaskID) || r.TaskRevision < 0 {
		return cerr("INVALID_REQUEST", "invalid review-result identity")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleReviewer {
		return cerr("INVALID_REQUEST", "review-result createdByRole must be reviewer")
	}
	if !sha256RE.MatchString(r.ReviewRequestSha256) || !sha256RE.MatchString(r.HandoffSha256) {
		return cerr("INVALID_REQUEST", "review-result record hashes must be 64 lowercase hex")
	}
	if !gitShaRE.MatchString(r.HeadSha) {
		return cerr("INVALID_REQUEST", "headSha must be a full 40-hex commit SHA")
	}
	if r.Verdict != VerdictPass && r.Verdict != VerdictPassWithNotes && r.Verdict != VerdictChangesRequested {
		return cerr("INVALID_REQUEST", "verdict must be PASS, PASS_WITH_NONBLOCKING_NOTES, or CHANGES_REQUESTED")
	}
	if err := boundedList(len(r.Findings), "findings"); err != nil {
		return err
	}
	seen := map[string]bool{}
	blocking := false
	for _, f := range r.Findings {
		if f.ID == "" || len(f.ID) > maxShortString || seen[f.ID] {
			return cerr("INVALID_REQUEST", "finding ids must be unique and bounded")
		}
		seen[f.ID] = true
		if f.Severity != "blocker" && f.Severity != "major" && f.Severity != "minor" && f.Severity != "note" {
			return cerr("INVALID_REQUEST", "finding severity must be blocker|major|minor|note")
		}
		if f.Severity == "blocker" || f.Severity == "major" {
			blocking = true
		}
		if err := boundedString(f.Summary, maxLongString, "finding summary"); err != nil {
			return err
		}
		if err := boundedString(f.Evidence, maxLongString, "finding evidence"); err != nil {
			return err
		}
	}
	if passingVerdict(r.Verdict) && blocking {
		return cerr("INVALID_REQUEST", "a passing verdict cannot carry blocker or major findings")
	}
	if r.Verdict == VerdictChangesRequested && !blocking {
		return cerr("INVALID_REQUEST", "CHANGES_REQUESTED requires at least one blocker or major finding")
	}
	return boundedString(r.RecommendedDisposition, maxLongString, "recommendedDisposition")
}

// FinalAcceptance (coord.final-acceptance.v0) binds the reviewed head,
// handoff, review result, and a complete artifact manifest.
type FinalAcceptance struct {
	SchemaVersion      string           `json:"schemaVersion"`
	RecordType         string           `json:"recordType"`
	WorkstreamID       string           `json:"workstreamId"`
	TaskID             string           `json:"taskId"`
	TaskRevision       int64            `json:"taskRevision"`
	CreatedAt          string           `json:"createdAt"`
	CreatedByRole      string           `json:"createdByRole"`
	HeadSha            string           `json:"headSha"`
	HandoffSha256      string           `json:"handoffSha256"`
	ReviewResultSha256 string           `json:"reviewResultSha256"`
	Gates              []CheckResult    `json:"gates"`
	ArtifactManifest   []OutputArtifact `json:"artifactManifest"`
}

func (r *FinalAcceptance) validate() error {
	if r.SchemaVersion != "coord.final-acceptance.v0" || r.RecordType != "final-acceptance" {
		return cerr("INVALID_REQUEST", "final-acceptance schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) || !taskIDRE.MatchString(r.TaskID) || r.TaskRevision < 0 {
		return cerr("INVALID_REQUEST", "invalid final-acceptance identity")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleOrchestrator {
		return cerr("INVALID_REQUEST", "final-acceptance createdByRole must be orchestrator")
	}
	if !gitShaRE.MatchString(r.HeadSha) {
		return cerr("INVALID_REQUEST", "headSha must be a full 40-hex commit SHA")
	}
	if !sha256RE.MatchString(r.HandoffSha256) || !sha256RE.MatchString(r.ReviewResultSha256) {
		return cerr("INVALID_REQUEST", "final-acceptance record hashes must be 64 lowercase hex")
	}
	if err := boundedList(len(r.Gates), "gates"); err != nil {
		return err
	}
	for _, g := range r.Gates {
		if g.Name == "" || len(g.Name) > maxShortString || (g.Status != "pass" && g.Status != "skipped") {
			return cerr("INVALID_REQUEST", "acceptance gates must be named and pass|skipped")
		}
		if err := boundedString(g.Detail, maxLongString, "gate detail"); err != nil {
			return err
		}
	}
	if len(r.ArtifactManifest) == 0 {
		return cerr("INVALID_REQUEST", "final-acceptance requires a complete artifact manifest")
	}
	if err := boundedList(len(r.ArtifactManifest), "artifactManifest"); err != nil {
		return err
	}
	for _, a := range r.ArtifactManifest {
		if a.Path == "" || len(a.Path) > maxLongString || !sha256RE.MatchString(a.Sha256) {
			return cerr("INVALID_REQUEST", "artifact manifest entries require a path and 64-hex sha256")
		}
	}
	return nil
}

// ReleaseHandoff (coord.release-handoff.v0) prepares the accepted head for
// release-conductor consumption. It never merges or deploys.
type ReleaseHandoff struct {
	SchemaVersion    string   `json:"schemaVersion"`
	RecordType       string   `json:"recordType"`
	WorkstreamID     string   `json:"workstreamId"`
	TaskID           string   `json:"taskId"`
	TaskRevision     int64    `json:"taskRevision"`
	CreatedAt        string   `json:"createdAt"`
	CreatedByRole    string   `json:"createdByRole"`
	AcceptanceSha256 string   `json:"acceptanceSha256"`
	HeadSha          string   `json:"headSha"`
	BaseSha          string   `json:"baseSha"`
	Branch           string   `json:"branch"`
	Notes            []string `json:"notes"`
}

func (r *ReleaseHandoff) validate() error {
	if r.SchemaVersion != "coord.release-handoff.v0" || r.RecordType != "release-handoff" {
		return cerr("INVALID_REQUEST", "release-handoff schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) || !taskIDRE.MatchString(r.TaskID) || r.TaskRevision < 0 {
		return cerr("INVALID_REQUEST", "invalid release-handoff identity")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleOrchestrator {
		return cerr("INVALID_REQUEST", "release-handoff createdByRole must be orchestrator")
	}
	if !sha256RE.MatchString(r.AcceptanceSha256) {
		return cerr("INVALID_REQUEST", "acceptanceSha256 must be 64 lowercase hex")
	}
	if !gitShaRE.MatchString(r.HeadSha) || !gitShaRE.MatchString(r.BaseSha) {
		return cerr("INVALID_REQUEST", "headSha and baseSha must be full 40-hex commit SHAs")
	}
	if r.Branch == "" || len(r.Branch) > maxShortString {
		return cerr("INVALID_REQUEST", "branch is required and bounded")
	}
	if err := boundedList(len(r.Notes), "notes"); err != nil {
		return err
	}
	for _, s := range r.Notes {
		if err := boundedString(s, maxLongString, "release note"); err != nil {
			return err
		}
	}
	return nil
}

// WorkstreamContract (coord.workstream-contract.v0) creates the workstream and
// binds every role to one authenticated client identity.
type WorkstreamContract struct {
	SchemaVersion string        `json:"schemaVersion"`
	RecordType    string        `json:"recordType"`
	WorkstreamID  string        `json:"workstreamId"`
	CreatedAt     string        `json:"createdAt"`
	CreatedByRole string        `json:"createdByRole"`
	Repository    string        `json:"repository"`
	Description   string        `json:"description"`
	Roles         []RoleBinding `json:"roles"`
}

func (r *WorkstreamContract) validate() error {
	if r.SchemaVersion != "coord.workstream-contract.v0" || r.RecordType != "workstream-contract" {
		return cerr("INVALID_REQUEST", "workstream-contract schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) {
		return cerr("INVALID_REQUEST", "invalid workstreamId")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleOperator {
		return cerr("INVALID_REQUEST", "workstream-contract createdByRole must be operator")
	}
	if !filepath.IsAbs(r.Repository) {
		return cerr("INVALID_REQUEST", "repository must be an absolute path")
	}
	if err := boundedString(r.Description, maxLongString, "description"); err != nil {
		return err
	}
	if len(r.Roles) == 0 || len(r.Roles) > maxRolesPerWS {
		return cerr("INVALID_REQUEST", fmt.Sprintf("roles must contain 1..%d bindings", maxRolesPerWS))
	}
	seenRole, seenClient := map[string]bool{}, map[string]bool{}
	for _, b := range r.Roles {
		if !validRole(b.Role) {
			return cerr("INVALID_REQUEST", "unknown role in contract")
		}
		if b.ClientID == "" || len(b.ClientID) > 128 || strings.ContainsAny(b.ClientID, "\x00\r\n\t") {
			return cerr("INVALID_REQUEST", "role clientId is required and bounded")
		}
		if seenRole[b.Role] {
			return cerr("INVALID_REQUEST", "each role may be bound at most once")
		}
		if seenClient[b.ClientID] {
			return cerr("INVALID_REQUEST", "a client may hold at most one role")
		}
		seenRole[b.Role], seenClient[b.ClientID] = true, true
	}
	return nil
}

// LeaseTransfer (coord.lease-transfer.v0) is the only path by which the
// read-only reviewer gains a scoped, bounded write lease.
type LeaseTransfer struct {
	SchemaVersion string   `json:"schemaVersion"`
	RecordType    string   `json:"recordType"`
	WorkstreamID  string   `json:"workstreamId"`
	TaskID        string   `json:"taskId"`
	TaskRevision  int64    `json:"taskRevision"`
	CreatedAt     string   `json:"createdAt"`
	CreatedByRole string   `json:"createdByRole"`
	ToRole        string   `json:"toRole"`
	Scope         []string `json:"scope"`
	ExpiresAt     string   `json:"expiresAt"`
	Reason        string   `json:"reason"`
}

func (r *LeaseTransfer) validate() error {
	if r.SchemaVersion != "coord.lease-transfer.v0" || r.RecordType != "lease-transfer" {
		return cerr("INVALID_REQUEST", "lease-transfer schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) || !taskIDRE.MatchString(r.TaskID) || r.TaskRevision < 0 {
		return cerr("INVALID_REQUEST", "invalid lease-transfer identity")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleOrchestrator {
		return cerr("INVALID_REQUEST", "lease-transfer createdByRole must be orchestrator")
	}
	if r.ToRole != RoleReviewer {
		return cerr("INVALID_REQUEST", "a lease transfer may only target the reviewer")
	}
	if len(r.Scope) == 0 {
		return cerr("INVALID_REQUEST", "lease-transfer scope paths are required")
	}
	if err := boundedList(len(r.Scope), "scope"); err != nil {
		return err
	}
	for _, p := range r.Scope {
		if !filepath.IsAbs(p) || len(p) > maxLongString {
			return cerr("INVALID_REQUEST", "lease-transfer scope paths must be absolute and bounded")
		}
	}
	if err := validRFC3339(r.ExpiresAt, "expiresAt"); err != nil {
		return err
	}
	exp, _ := time.Parse(time.RFC3339, r.ExpiresAt)
	crt, _ := time.Parse(time.RFC3339, r.CreatedAt)
	if !exp.After(crt) || exp.Sub(crt) > 24*time.Hour {
		return cerr("INVALID_REQUEST", "lease-transfer expiry must be within 24 hours of creation")
	}
	return boundedString(r.Reason, maxLongString, "reason")
}

// PlanReference (coord.plan-reference.v0) pins a frozen plan by hash. It is a
// supporting artifact and drives no transition.
type PlanReference struct {
	SchemaVersion string `json:"schemaVersion"`
	RecordType    string `json:"recordType"`
	WorkstreamID  string `json:"workstreamId"`
	CreatedAt     string `json:"createdAt"`
	CreatedByRole string `json:"createdByRole"`
	Title         string `json:"title"`
	Path          string `json:"path"`
	Sha256        string `json:"sha256"`
	Revision      int64  `json:"revision"`
}

func (r *PlanReference) validate() error {
	if r.SchemaVersion != "coord.plan-reference.v0" || r.RecordType != "plan-reference" {
		return cerr("INVALID_REQUEST", "plan-reference schema/recordType mismatch")
	}
	if !workstreamIDRE.MatchString(r.WorkstreamID) || r.Revision < 0 {
		return cerr("INVALID_REQUEST", "invalid plan-reference identity")
	}
	if err := validRFC3339(r.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if r.CreatedByRole != RoleOrchestrator {
		return cerr("INVALID_REQUEST", "plan-reference createdByRole must be orchestrator")
	}
	if !sha256RE.MatchString(r.Sha256) {
		return cerr("INVALID_REQUEST", "plan-reference sha256 must be 64 lowercase hex")
	}
	if err := boundedString(r.Title, maxShortString, "title"); err != nil {
		return err
	}
	return boundedString(r.Path, maxLongString, "path")
}

// ---- controller-authored records ----------------------------------------

// TaskClaim (coord.task-claim.v0) is constructed by the controller when the
// assigned builder claims a published task.
type TaskClaim struct {
	SchemaVersion string `json:"schemaVersion"`
	RecordType    string `json:"recordType"`
	WorkstreamID  string `json:"workstreamId"`
	TaskID        string `json:"taskId"`
	TaskRevision  int64  `json:"taskRevision"`
	CreatedAt     string `json:"createdAt"`
	CreatedByRole string `json:"createdByRole"`
	ClaimedBy     string `json:"claimedBy"`
	LeaseID       string `json:"leaseId"`
	Worktree      string `json:"worktree"`
	AssignmentSha string `json:"assignmentSha256"`
}

// WriteLeaseRecord (coord.write-lease.v0) is the durable exclusive lease fact.
type WriteLeaseRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	RecordType    string `json:"recordType"`
	WorkstreamID  string `json:"workstreamId"`
	TaskID        string `json:"taskId"`
	TaskRevision  int64  `json:"taskRevision"`
	CreatedAt     string `json:"createdAt"`
	CreatedByRole string `json:"createdByRole"`
	LeaseID       string `json:"leaseId"`
	HolderRole    string `json:"holderRole"`
	HolderClient  string `json:"holderClient"`
	Scope         string `json:"scope"`
	Exclusive     bool   `json:"exclusive"`
	Status        string `json:"status"` // active | released
}

// TaskAcknowledgement (coord.task-acknowledgement.v0) records structured
// receipt of a delivered task pointer, bound by request id and artifact hash.
type TaskAcknowledgement struct {
	SchemaVersion  string `json:"schemaVersion"`
	RecordType     string `json:"recordType"`
	WorkstreamID   string `json:"workstreamId"`
	TaskID         string `json:"taskId"`
	TaskRevision   int64  `json:"taskRevision"`
	CreatedAt      string `json:"createdAt"`
	CreatedByRole  string `json:"createdByRole"`
	RequestID      string `json:"requestId"`
	ArtifactSha256 string `json:"artifactSha256"`
	AcknowledgedBy string `json:"acknowledgedBy"`
}

// LeaseReturn (coord.lease-return.v0) records the reviewer returning a scoped
// small-fix lease before any verdict.
type LeaseReturn struct {
	SchemaVersion string `json:"schemaVersion"`
	RecordType    string `json:"recordType"`
	WorkstreamID  string `json:"workstreamId"`
	TaskID        string `json:"taskId"`
	TaskRevision  int64  `json:"taskRevision"`
	CreatedAt     string `json:"createdAt"`
	CreatedByRole string `json:"createdByRole"`
	LeaseID       string `json:"leaseId"`
	TransferSha   string `json:"transferSha256"`
}

// WorkspaceCheckpoint (coord.workspace-checkpoint.v0) characterizes a current
// Cockpit workspace from controller-owned structural metadata only. It never
// reads pane output, titles, or provider transcripts.
type WorkspaceCheckpoint struct {
	SchemaVersion string           `json:"schemaVersion"`
	RecordType    string           `json:"recordType"`
	WorkstreamID  string           `json:"workstreamId"`
	CreatedAt     string           `json:"createdAt"`
	CreatedByRole string           `json:"createdByRole"`
	WorkspaceRef  string           `json:"workspaceRef"`
	Reason        string           `json:"reason"`
	Panes         []CheckpointPane `json:"panes"`
}

type CheckpointPane struct {
	PaneRef    string `json:"paneRef"`
	Generation int64  `json:"generation"`
	Version    int64  `json:"version"`
	Badge      string `json:"badge"`
	Fenced     bool   `json:"fenced"`
}

// recordSpec binds a stored record type to its validator and the role allowed
// to publish it through the generic artifact path.
type recordSpec struct {
	schemaVersion string
	publishRole   string // role allowed via artifact_publish ("" = never generic)
	validate      func(raw []byte) error
}

var recordSpecs = map[string]recordSpec{
	"task-assignment": {"coord.task-assignment.v0", "", func(raw []byte) error {
		var r TaskAssignment
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "task-assignment: "+err.Error())
		}
		return r.validate()
	}},
	"builder-handoff": {"coord.builder-handoff.v0", "", func(raw []byte) error {
		var r BuilderHandoff
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "builder-handoff: "+err.Error())
		}
		return r.validate()
	}},
	"review-request": {"coord.review-request.v0", "", func(raw []byte) error {
		var r ReviewRequest
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "review-request: "+err.Error())
		}
		return r.validate()
	}},
	"review-result": {"coord.review-result.v0", "", func(raw []byte) error {
		var r ReviewResult
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "review-result: "+err.Error())
		}
		return r.validate()
	}},
	"final-acceptance": {"coord.final-acceptance.v0", "", func(raw []byte) error {
		var r FinalAcceptance
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "final-acceptance: "+err.Error())
		}
		return r.validate()
	}},
	"release-handoff": {"coord.release-handoff.v0", "", func(raw []byte) error {
		var r ReleaseHandoff
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "release-handoff: "+err.Error())
		}
		return r.validate()
	}},
	"workstream-contract": {"coord.workstream-contract.v0", "", func(raw []byte) error {
		var r WorkstreamContract
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "workstream-contract: "+err.Error())
		}
		return r.validate()
	}},
	"lease-transfer": {"coord.lease-transfer.v0", "", func(raw []byte) error {
		var r LeaseTransfer
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "lease-transfer: "+err.Error())
		}
		return r.validate()
	}},
	"plan-reference": {"coord.plan-reference.v0", RoleOrchestrator, func(raw []byte) error {
		var r PlanReference
		if err := strictDecode(raw, &r); err != nil {
			return cerr("INVALID_REQUEST", "plan-reference: "+err.Error())
		}
		return r.validate()
	}},
}
