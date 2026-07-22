package coord

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// recordMutation decodes the common record-carrying mutation envelope and
// binds the idempotency intent to the canonical record bytes.
func decodeMutation(params json.RawMessage) (*mutation, []byte, error) {
	var p mutation
	if err := strictDecode(params, &p); err != nil {
		return nil, nil, cerr("INVALID_REQUEST", "invalid params")
	}
	if err := validateCanonicalRecord(p.Record); err != nil {
		return nil, nil, err
	}
	return &p, canonicalBytes(p.Record), nil
}

func peekRecordType(canon []byte) string {
	var head struct {
		RecordType string `json:"recordType"`
	}
	_ = json.Unmarshal(canon, &head)
	return head.RecordType
}

// ---- workstream_create ---------------------------------------------------

func (s *Service) workstreamCreate(sess Session, params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID   string          `json:"workstreamId"`
		IdempotencyKey string          `json:"idempotencyKey"`
		Record         json.RawMessage `json:"record"`
	}
	if err := strictDecode(params, &p); err != nil {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	if !sess.Fixed {
		return nil, cerr("PERMISSION_DENIED", "coordination mutations require a credential-pinned client identity")
	}
	if !workstreamIDRE.MatchString(p.WorkstreamID) {
		return nil, cerr("INVALID_REQUEST", "invalid workstreamId")
	}
	now := s.now()
	if err := validIdempotencyKey(p.IdempotencyKey, now); err != nil {
		return nil, err
	}
	if err := validateCanonicalRecord(p.Record); err != nil {
		return nil, err
	}
	canon := canonicalBytes(p.Record)
	var contract WorkstreamContract
	if err := strictDecode(canon, &contract); err != nil {
		return nil, cerr("INVALID_REQUEST", "workstream-contract: "+err.Error())
	}
	if err := contract.validate(); err != nil {
		return nil, err
	}
	if contract.WorkstreamID != p.WorkstreamID {
		return nil, cerr("INVALID_REQUEST", "contract workstreamId mismatch")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = tx.Rollback(); return nil, e }
	intentSha := sha256Hex([]byte("coordination.workstream_create\x00" + p.WorkstreamID + "\x00" + sha256Hex(canon)))
	var priorIntent, priorResult string
	err = tx.QueryRow("SELECT intent_sha,result FROM coord_idempotency WHERE caller=? AND method=? AND idem_key=?", sess.Caller, "coordination.workstream_create", p.IdempotencyKey).Scan(&priorIntent, &priorResult)
	if err == nil {
		_ = tx.Rollback()
		if priorIntent != intentSha {
			return nil, cerr("IDEMPOTENCY_CONFLICT", "idempotency key has a different intent")
		}
		var prior map[string]any
		_ = json.Unmarshal([]byte(priorResult), &prior)
		prior["replayed"] = true
		return prior, nil
	}
	if err != sql.ErrNoRows {
		return fail(err)
	}
	var exists int
	if err = tx.QueryRow("SELECT count(*) FROM coord_workstreams WHERE id=?", p.WorkstreamID).Scan(&exists); err != nil {
		return fail(err)
	}
	if exists != 0 {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "workstream already exists"))
	}
	m := &mutState{tx: tx, ws: p.WorkstreamID, caller: sess.Caller, role: RoleOperator, revision: 1, at: now}
	if _, err = tx.Exec("INSERT INTO coord_workstreams(id,revision,contract_sha,repository,created_at) VALUES(?,1,'',?,?)", p.WorkstreamID, contract.Repository, now.Unix()); err != nil {
		return fail(err)
	}
	for _, b := range contract.Roles {
		if _, err = tx.Exec("INSERT INTO coord_roles(workstream_id,role,client_id) VALUES(?,?,?)", p.WorkstreamID, b.Role, b.ClientID); err != nil {
			return fail(err)
		}
	}
	ref, sha, err := s.publishRecord(m, "workstream-contract", "", 0, RoleOperator, canon, "")
	if err != nil {
		return fail(err)
	}
	if _, err = tx.Exec("UPDATE coord_workstreams SET contract_sha=? WHERE id=?", sha, p.WorkstreamID); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "workstream.created", "", sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"workstreamId": p.WorkstreamID, "recordRef": ref, "contractSha256": sha}
	return s.finishMutation(m, sess, "coordination.workstream_create", p.IdempotencyKey, intentSha, result, seq)
}

// ---- task_publish (initial and single correction) ------------------------

func (s *Service) taskPublish(sess Session, params json.RawMessage) (any, error) {
	p, canon, err := decodeMutation(params)
	if err != nil {
		return nil, err
	}
	var a TaskAssignment
	if err := strictDecode(canon, &a); err != nil {
		return nil, cerr("INVALID_REQUEST", "task-assignment: "+err.Error())
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.WorkstreamID != p.WorkstreamID {
		return nil, cerr("INVALID_REQUEST", "record workstreamId mismatch")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, sha256Hex(canon))
	m, aux, err := s.beginMutation(sess, "coordination.task_publish", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleOrchestrator)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var taskCount int
	if err = m.tx.QueryRow("SELECT count(*) FROM coord_tasks WHERE workstream_id=?", p.WorkstreamID).Scan(&taskCount); err != nil {
		return fail(err)
	}
	if taskCount >= maxTasksPerWS {
		return fail(cerr("CAPABILITY_ABSENT", "workstream task limit reached"))
	}
	var exists int
	if err = m.tx.QueryRow("SELECT count(*) FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, a.TaskID, a.Revision).Scan(&exists); err != nil {
		return fail(err)
	}
	if exists != 0 {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "task revision already published"))
	}
	eventKind := "task.published"
	var correctionOf any
	if a.CorrectionOf != nil {
		eventKind = "correction.published"
		var priorStatus, priorReviewResult string
		err = m.tx.QueryRow("SELECT status,review_result_sha FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, a.TaskID, a.CorrectionOf.Revision).Scan(&priorStatus, &priorReviewResult)
		if err == sql.ErrNoRows {
			return fail(cerr("TARGET_NOT_FOUND", "correction references an unknown task revision"))
		}
		if err != nil {
			return fail(err)
		}
		if priorStatus != StatusReviewedChanges {
			return fail(cerr("CONFLICT_MATERIAL_STATE", "correction requires a reviewed-changes-requested predecessor"))
		}
		if priorReviewResult != a.CorrectionOf.ReviewResultSha256 {
			return fail(cerr("CONFLICT_MATERIAL_STATE", "correction reviewResultSha256 does not match the recorded review result"))
		}
		reviewBytes, rerr := s.readArtifact(m.tx, priorReviewResult)
		if rerr != nil {
			return fail(rerr)
		}
		var review ReviewResult
		if strictDecode(reviewBytes, &review) != nil {
			return fail(cerr("CONTROLLER_NOT_READY", "stored review result failed schema validation"))
		}
		known := map[string]bool{}
		for _, f := range review.Findings {
			known[f.ID] = true
		}
		for _, id := range a.CorrectionOf.FindingIDs {
			if !known[id] {
				return fail(cerr("INVALID_REQUEST", "correction references unknown finding id "+id))
			}
		}
		correctionOf = a.CorrectionOf.Revision
	}
	ref, sha, err := s.publishRecord(m, "task-assignment", a.TaskID, a.Revision, RoleOrchestrator, canon, "")
	if err != nil {
		return fail(err)
	}
	seededDep := ""
	if a.Pins.SeededInputCapability != nil {
		seededDep = a.Pins.SeededInputCapability.Sha
	}
	if _, err = m.tx.Exec(`INSERT INTO coord_tasks(workstream_id,task_id,revision,status,assignment_sha,plan_sha,base_sha,seeded_dep_sha,worktree,branch,lease_id,correction_of_revision)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.WorkstreamID, a.TaskID, a.Revision, StatusPublished, sha, a.Pins.Plan.Sha256, a.Pins.Base.Sha, seededDep, a.Authority.Worktree, a.Authority.Branch, a.Authority.WriteLease.LeaseID, correctionOf); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, eventKind, a.TaskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"taskId": a.TaskID, "taskRevision": a.Revision, "recordRef": ref, "assignmentSha256": sha, "taskStatus": StatusPublished}
	return s.finishMutation(m, sess, "coordination.task_publish", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

// ---- task_claim (builder acquires the sole write lease) -------------------

func (s *Service) taskClaim(sess Session, params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID     string `json:"workstreamId"`
		ExpectedRevision int64  `json:"expectedRevision"`
		IdempotencyKey   string `json:"idempotencyKey"`
		TaskID           string `json:"taskId"`
		TaskRevision     int64  `json:"taskRevision"`
	}
	if err := strictDecode(params, &p); err != nil || !taskIDRE.MatchString(p.TaskID) || p.TaskRevision < 0 {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s\x00%d", p.WorkstreamID, p.ExpectedRevision, p.TaskID, p.TaskRevision)
	m, aux, err := s.beginMutation(sess, "coordination.task_claim", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleBuilder)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var status, assignmentSha, worktree, leaseID string
	err = m.tx.QueryRow("SELECT status,assignment_sha,worktree,lease_id FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, p.TaskID, p.TaskRevision).Scan(&status, &assignmentSha, &worktree, &leaseID)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "task not found"))
	}
	if err != nil {
		return fail(err)
	}
	if status != StatusPublished && status != StatusAcknowledged {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "task is not claimable in status "+status))
	}
	var activeLeases int
	if err = m.tx.QueryRow("SELECT count(*) FROM coord_leases WHERE workstream_id=? AND status='active'", p.WorkstreamID).Scan(&activeLeases); err != nil {
		return fail(err)
	}
	if activeLeases != 0 {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "another write lease is already active"))
	}
	at := m.at.UTC().Format(time.RFC3339)
	claim := TaskClaim{
		SchemaVersion: "coord.task-claim.v0", RecordType: "task-claim",
		WorkstreamID: p.WorkstreamID, TaskID: p.TaskID, TaskRevision: p.TaskRevision,
		CreatedAt: at, CreatedByRole: RoleBuilder, ClaimedBy: sess.Caller,
		LeaseID: leaseID, Worktree: worktree, AssignmentSha: assignmentSha,
	}
	claimBytes, _ := json.Marshal(claim)
	claimRef, claimSha, err := s.publishRecord(m, "task-claim", p.TaskID, p.TaskRevision, RoleBuilder, claimBytes, assignmentSha)
	if err != nil {
		return fail(err)
	}
	lease := WriteLeaseRecord{
		SchemaVersion: "coord.write-lease.v0", RecordType: "write-lease",
		WorkstreamID: p.WorkstreamID, TaskID: p.TaskID, TaskRevision: p.TaskRevision,
		CreatedAt: at, CreatedByRole: RoleBuilder, LeaseID: leaseID,
		HolderRole: RoleBuilder, HolderClient: sess.Caller, Scope: worktree,
		Exclusive: true, Status: "active",
	}
	leaseBytes, _ := json.Marshal(lease)
	_, leaseSha, err := s.publishRecord(m, "write-lease", p.TaskID, p.TaskRevision, RoleBuilder, leaseBytes, claimSha)
	if err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("INSERT INTO coord_leases(workstream_id,lease_id,task_id,task_revision,holder_role,holder_client,scope,status,acquired_at) VALUES(?,?,?,?,?,?,?,'active',?)",
		p.WorkstreamID, leaseID, p.TaskID, p.TaskRevision, RoleBuilder, sess.Caller, worktree, m.at.Unix()); err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_tasks SET status=? WHERE workstream_id=? AND task_id=? AND revision=?", StatusClaimed, p.WorkstreamID, p.TaskID, p.TaskRevision); err != nil {
		return fail(err)
	}
	if _, err = s.addEvent(m, "task.claimed", p.TaskID, claimSha); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "lease.acquired", p.TaskID, leaseSha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"taskId": p.TaskID, "taskRevision": p.TaskRevision, "taskStatus": StatusClaimed, "leaseId": leaseID, "claimRef": claimRef, "claimSha256": claimSha, "leaseSha256": leaseSha}
	return s.finishMutation(m, sess, "coordination.task_claim", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

// ---- artifact_publish (supporting artifacts only) ------------------------

func (s *Service) artifactPublish(sess Session, params json.RawMessage) (any, error) {
	p, canon, err := decodeMutation(params)
	if err != nil {
		return nil, err
	}
	recordType := peekRecordType(canon)
	spec, ok := recordSpecs[recordType]
	if !ok {
		return nil, cerr("INVALID_REQUEST", "unknown recordType")
	}
	if spec.publishRole == "" {
		return nil, cerr("PERMISSION_DENIED", "recordType "+recordType+" must be published through its dedicated operation")
	}
	if err := spec.validate(canon); err != nil {
		return nil, err
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, sha256Hex(canon))
	m, aux, err := s.beginMutation(sess, "coordination.artifact_publish", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, spec.publishRole)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	ref, sha, err := s.publishRecord(m, recordType, "", 0, m.role, canon, "")
	if err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "artifact.published", "", sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"recordRef": ref, "sha256": sha, "recordType": recordType}
	return s.finishMutation(m, sess, "coordination.artifact_publish", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

// ---- handoff_submit ------------------------------------------------------

func (s *Service) handoffSubmit(sess Session, params json.RawMessage) (any, error) {
	p, canon, err := decodeMutation(params)
	if err != nil {
		return nil, err
	}
	var h BuilderHandoff
	if err := strictDecode(canon, &h); err != nil {
		return nil, cerr("INVALID_REQUEST", "builder-handoff: "+err.Error())
	}
	if err := h.validate(); err != nil {
		return nil, err
	}
	if h.WorkstreamID != p.WorkstreamID {
		return nil, cerr("INVALID_REQUEST", "record workstreamId mismatch")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, sha256Hex(canon))
	m, aux, err := s.beginMutation(sess, "coordination.handoff_submit", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleBuilder)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var status, planSha, baseSha, seededDep, worktree, branch, leaseID string
	err = m.tx.QueryRow("SELECT status,plan_sha,base_sha,seeded_dep_sha,worktree,branch,lease_id FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, h.TaskID, h.TaskRevision).Scan(&status, &planSha, &baseSha, &seededDep, &worktree, &branch, &leaseID)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "task not found"))
	}
	if err != nil {
		return fail(err)
	}
	if status != StatusClaimed {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "handoff requires a claimed task, task is "+status))
	}
	if h.PlanSha256 != planSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "handoff planSha256 does not match the pinned plan"))
	}
	if h.BaseSha != baseSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "handoff baseSha does not match the pinned base"))
	}
	if h.HeadSha == baseSha {
		return fail(cerr("INVALID_REQUEST", "handoff head must contain at least one commit beyond base"))
	}
	if h.Branch != branch || h.Worktree != worktree {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "handoff branch/worktree do not match the assignment"))
	}
	if seededDep != "" && h.SeededInputDependencySha != seededDep {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "handoff seededInputDependencySha does not match the pinned dependency"))
	}
	var holder string
	err = m.tx.QueryRow("SELECT holder_client FROM coord_leases WHERE workstream_id=? AND lease_id=? AND status='active'", p.WorkstreamID, leaseID).Scan(&holder)
	if err == sql.ErrNoRows {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "no active builder lease for this task"))
	}
	if err != nil {
		return fail(err)
	}
	if holder != sess.Caller {
		return fail(cerr("PERMISSION_DENIED", "the builder lease is held by another client"))
	}
	ref, sha, err := s.publishRecord(m, "builder-handoff", h.TaskID, h.TaskRevision, RoleBuilder, canon, "")
	if err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_tasks SET status=?,head_sha=?,handoff_sha=? WHERE workstream_id=? AND task_id=? AND revision=?", StatusHandoffSubmitted, h.HeadSha, sha, p.WorkstreamID, h.TaskID, h.TaskRevision); err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_leases SET status='released',released_at=? WHERE workstream_id=? AND lease_id=?", m.at.Unix(), p.WorkstreamID, leaseID); err != nil {
		return fail(err)
	}
	if _, err = s.addEvent(m, "handoff.submitted", h.TaskID, sha); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "lease.released", h.TaskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"taskId": h.TaskID, "taskRevision": h.TaskRevision, "taskStatus": StatusHandoffSubmitted, "recordRef": ref, "handoffSha256": sha, "headSha": h.HeadSha, "leaseReleased": true}
	return s.finishMutation(m, sess, "coordination.handoff_submit", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

// ---- review_request ------------------------------------------------------

func (s *Service) reviewRequest(sess Session, params json.RawMessage) (any, error) {
	p, canon, err := decodeMutation(params)
	if err != nil {
		return nil, err
	}
	var r ReviewRequest
	if err := strictDecode(canon, &r); err != nil {
		return nil, cerr("INVALID_REQUEST", "review-request: "+err.Error())
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	if r.WorkstreamID != p.WorkstreamID {
		return nil, cerr("INVALID_REQUEST", "record workstreamId mismatch")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, sha256Hex(canon))
	m, aux, err := s.beginMutation(sess, "coordination.review_request", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleOrchestrator)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var status, headSha, handoffSha, baseSha string
	err = m.tx.QueryRow("SELECT status,head_sha,handoff_sha,base_sha FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, r.TaskID, r.TaskRevision).Scan(&status, &headSha, &handoffSha, &baseSha)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "task not found"))
	}
	if err != nil {
		return fail(err)
	}
	if status != StatusHandoffSubmitted {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "review request requires a submitted handoff, task is "+status))
	}
	if r.HandoffSha256 != handoffSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "review request handoffSha256 does not match the recorded handoff"))
	}
	if r.HeadSha != headSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "review request headSha does not match the handoff head"))
	}
	if r.BaseSha != baseSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "review request baseSha does not match the pinned base"))
	}
	ref, sha, err := s.publishRecord(m, "review-request", r.TaskID, r.TaskRevision, RoleOrchestrator, canon, handoffSha)
	if err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_tasks SET status=?,review_request_sha=? WHERE workstream_id=? AND task_id=? AND revision=?", StatusReviewRequested, sha, p.WorkstreamID, r.TaskID, r.TaskRevision); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "review.requested", r.TaskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"taskId": r.TaskID, "taskRevision": r.TaskRevision, "taskStatus": StatusReviewRequested, "recordRef": ref, "reviewRequestSha256": sha, "headSha": headSha}
	return s.finishMutation(m, sess, "coordination.review_request", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

// ---- review_submit -------------------------------------------------------

func (s *Service) reviewSubmit(sess Session, params json.RawMessage) (any, error) {
	p, canon, err := decodeMutation(params)
	if err != nil {
		return nil, err
	}
	var r ReviewResult
	if err := strictDecode(canon, &r); err != nil {
		return nil, cerr("INVALID_REQUEST", "review-result: "+err.Error())
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	if r.WorkstreamID != p.WorkstreamID {
		return nil, cerr("INVALID_REQUEST", "record workstreamId mismatch")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, sha256Hex(canon))
	m, aux, err := s.beginMutation(sess, "coordination.review_submit", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleReviewer)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var status, headSha, handoffSha, requestSha string
	err = m.tx.QueryRow("SELECT status,head_sha,handoff_sha,review_request_sha FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, r.TaskID, r.TaskRevision).Scan(&status, &headSha, &handoffSha, &requestSha)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "task not found"))
	}
	if err != nil {
		return fail(err)
	}
	if status != StatusReviewRequested {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "review submission requires a requested review, task is "+status))
	}
	if r.ReviewRequestSha256 != requestSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "review result reviewRequestSha256 does not match the recorded request"))
	}
	if r.HandoffSha256 != handoffSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "review result handoffSha256 does not match the recorded handoff"))
	}
	if r.HeadSha != headSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "review result headSha does not match the reviewed head"))
	}
	var activeLeases int
	if err = m.tx.QueryRow("SELECT count(*) FROM coord_leases WHERE workstream_id=? AND status='active'", p.WorkstreamID).Scan(&activeLeases); err != nil {
		return fail(err)
	}
	if activeLeases != 0 {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "an active write lease must be returned before a review verdict"))
	}
	newStatus := StatusReviewedChanges
	if passingVerdict(r.Verdict) {
		newStatus = StatusReviewedPass
	}
	ref, sha, err := s.publishRecord(m, "review-result", r.TaskID, r.TaskRevision, RoleReviewer, canon, requestSha)
	if err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_tasks SET status=?,review_result_sha=?,verdict=? WHERE workstream_id=? AND task_id=? AND revision=?", newStatus, sha, r.Verdict, p.WorkstreamID, r.TaskID, r.TaskRevision); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "review.submitted", r.TaskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"taskId": r.TaskID, "taskRevision": r.TaskRevision, "taskStatus": newStatus, "recordRef": ref, "reviewResultSha256": sha, "verdict": r.Verdict}
	return s.finishMutation(m, sess, "coordination.review_submit", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

// ---- lease_transfer / lease_return ---------------------------------------

func (s *Service) leaseTransfer(sess Session, params json.RawMessage) (any, error) {
	p, canon, err := decodeMutation(params)
	if err != nil {
		return nil, err
	}
	var t LeaseTransfer
	if err := strictDecode(canon, &t); err != nil {
		return nil, cerr("INVALID_REQUEST", "lease-transfer: "+err.Error())
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	if t.WorkstreamID != p.WorkstreamID {
		return nil, cerr("INVALID_REQUEST", "record workstreamId mismatch")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, sha256Hex(canon))
	m, aux, err := s.beginMutation(sess, "coordination.lease_transfer", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleOrchestrator)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var status, worktree string
	err = m.tx.QueryRow("SELECT status,worktree FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, t.TaskID, t.TaskRevision).Scan(&status, &worktree)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "task not found"))
	}
	if err != nil {
		return fail(err)
	}
	if status != StatusReviewRequested {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "a small-fix lease transfer requires an in-flight review"))
	}
	for _, path := range t.Scope {
		if path != worktree && !strings.HasPrefix(path, worktree+string(filepath.Separator)) {
			return fail(cerr("INVALID_REQUEST", "lease-transfer scope must stay inside the task worktree"))
		}
	}
	var reviewerClient string
	err = m.tx.QueryRow("SELECT client_id FROM coord_roles WHERE workstream_id=? AND role=?", p.WorkstreamID, RoleReviewer).Scan(&reviewerClient)
	if err == sql.ErrNoRows {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "no reviewer role is bound in this workstream"))
	}
	if err != nil {
		return fail(err)
	}
	var activeLeases int
	if err = m.tx.QueryRow("SELECT count(*) FROM coord_leases WHERE workstream_id=? AND status='active'", p.WorkstreamID).Scan(&activeLeases); err != nil {
		return fail(err)
	}
	if activeLeases != 0 {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "another write lease is already active"))
	}
	ref, sha, err := s.publishRecord(m, "lease-transfer", t.TaskID, t.TaskRevision, RoleOrchestrator, canon, "")
	if err != nil {
		return fail(err)
	}
	leaseID := "LT-" + sha[:16]
	scopeJSON, _ := json.Marshal(t.Scope)
	expires, _ := time.Parse(time.RFC3339, t.ExpiresAt)
	if _, err = m.tx.Exec("INSERT INTO coord_leases(workstream_id,lease_id,task_id,task_revision,holder_role,holder_client,scope,status,transfer_sha,expires_at,acquired_at) VALUES(?,?,?,?,?,?,?,'active',?,?,?)",
		p.WorkstreamID, leaseID, t.TaskID, t.TaskRevision, RoleReviewer, reviewerClient, string(scopeJSON), sha, expires.Unix(), m.at.Unix()); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "lease.transferred", t.TaskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"taskId": t.TaskID, "taskRevision": t.TaskRevision, "recordRef": ref, "transferSha256": sha, "leaseId": leaseID, "holderRole": RoleReviewer, "holderClient": reviewerClient}
	return s.finishMutation(m, sess, "coordination.lease_transfer", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

func (s *Service) leaseReturn(sess Session, params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID     string `json:"workstreamId"`
		ExpectedRevision int64  `json:"expectedRevision"`
		IdempotencyKey   string `json:"idempotencyKey"`
		LeaseID          string `json:"leaseId"`
	}
	if err := strictDecode(params, &p); err != nil || !leaseIDRE.MatchString(p.LeaseID) {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, p.LeaseID)
	m, aux, err := s.beginMutation(sess, "coordination.lease_return", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleReviewer)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var holderClient, transferSha, taskID string
	var taskRev int64
	err = m.tx.QueryRow("SELECT holder_client,transfer_sha,task_id,task_revision FROM coord_leases WHERE workstream_id=? AND lease_id=? AND status='active'", p.WorkstreamID, p.LeaseID).Scan(&holderClient, &transferSha, &taskID, &taskRev)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "no active lease with this id"))
	}
	if err != nil {
		return fail(err)
	}
	if holderClient != sess.Caller {
		return fail(cerr("PERMISSION_DENIED", "the lease is held by another client"))
	}
	ret := LeaseReturn{
		SchemaVersion: "coord.lease-return.v0", RecordType: "lease-return",
		WorkstreamID: p.WorkstreamID, TaskID: taskID, TaskRevision: taskRev,
		CreatedAt: m.at.UTC().Format(time.RFC3339), CreatedByRole: RoleReviewer,
		LeaseID: p.LeaseID, TransferSha: transferSha,
	}
	retBytes, _ := json.Marshal(ret)
	ref, sha, err := s.publishRecord(m, "lease-return", taskID, taskRev, RoleReviewer, retBytes, transferSha)
	if err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_leases SET status='released',released_at=? WHERE workstream_id=? AND lease_id=?", m.at.Unix(), p.WorkstreamID, p.LeaseID); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "lease.returned", taskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"leaseId": p.LeaseID, "recordRef": ref, "returnSha256": sha}
	return s.finishMutation(m, sess, "coordination.lease_return", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

// ---- acceptance_submit / release_submit ----------------------------------

func (s *Service) acceptanceSubmit(sess Session, params json.RawMessage) (any, error) {
	p, canon, err := decodeMutation(params)
	if err != nil {
		return nil, err
	}
	var a FinalAcceptance
	if err := strictDecode(canon, &a); err != nil {
		return nil, cerr("INVALID_REQUEST", "final-acceptance: "+err.Error())
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.WorkstreamID != p.WorkstreamID {
		return nil, cerr("INVALID_REQUEST", "record workstreamId mismatch")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, sha256Hex(canon))
	m, aux, err := s.beginMutation(sess, "coordination.acceptance_submit", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleOrchestrator)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var status, headSha, handoffSha, reviewResultSha string
	err = m.tx.QueryRow("SELECT status,head_sha,handoff_sha,review_result_sha FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, a.TaskID, a.TaskRevision).Scan(&status, &headSha, &handoffSha, &reviewResultSha)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "task not found"))
	}
	if err != nil {
		return fail(err)
	}
	if status != StatusReviewedPass {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "acceptance requires a passing review, task is "+status))
	}
	if a.HeadSha != headSha || a.HandoffSha256 != handoffSha || a.ReviewResultSha256 != reviewResultSha {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "acceptance must bind the exact reviewed head, handoff, and review result"))
	}
	var activeLeases int
	if err = m.tx.QueryRow("SELECT count(*) FROM coord_leases WHERE workstream_id=? AND status='active'", p.WorkstreamID).Scan(&activeLeases); err != nil {
		return fail(err)
	}
	if activeLeases != 0 {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "acceptance requires no active write lease"))
	}
	ref, sha, err := s.publishRecord(m, "final-acceptance", a.TaskID, a.TaskRevision, RoleOrchestrator, canon, reviewResultSha)
	if err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_tasks SET status=?,acceptance_sha=? WHERE workstream_id=? AND task_id=? AND revision=?", StatusAccepted, sha, p.WorkstreamID, a.TaskID, a.TaskRevision); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "task.accepted", a.TaskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"taskId": a.TaskID, "taskRevision": a.TaskRevision, "taskStatus": StatusAccepted, "recordRef": ref, "acceptanceSha256": sha}
	return s.finishMutation(m, sess, "coordination.acceptance_submit", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

func (s *Service) releaseSubmit(sess Session, params json.RawMessage) (any, error) {
	p, canon, err := decodeMutation(params)
	if err != nil {
		return nil, err
	}
	var r ReleaseHandoff
	if err := strictDecode(canon, &r); err != nil {
		return nil, cerr("INVALID_REQUEST", "release-handoff: "+err.Error())
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	if r.WorkstreamID != p.WorkstreamID {
		return nil, cerr("INVALID_REQUEST", "record workstreamId mismatch")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, sha256Hex(canon))
	m, aux, err := s.beginMutation(sess, "coordination.release_submit", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleOrchestrator)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var status, headSha, baseSha, branch, acceptanceSha string
	err = m.tx.QueryRow("SELECT status,head_sha,base_sha,branch,acceptance_sha FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, r.TaskID, r.TaskRevision).Scan(&status, &headSha, &baseSha, &branch, &acceptanceSha)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "task not found"))
	}
	if err != nil {
		return fail(err)
	}
	if status != StatusAccepted {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "release handoff requires an accepted task, task is "+status))
	}
	if r.AcceptanceSha256 != acceptanceSha || r.HeadSha != headSha || r.BaseSha != baseSha || r.Branch != branch {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "release handoff must bind the exact accepted head, base, branch, and acceptance record"))
	}
	ref, sha, err := s.publishRecord(m, "release-handoff", r.TaskID, r.TaskRevision, RoleOrchestrator, canon, acceptanceSha)
	if err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_tasks SET status=?,release_sha=? WHERE workstream_id=? AND task_id=? AND revision=?", StatusReleased, sha, p.WorkstreamID, r.TaskID, r.TaskRevision); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "release.submitted", r.TaskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"taskId": r.TaskID, "taskRevision": r.TaskRevision, "taskStatus": StatusReleased, "recordRef": ref, "releaseSha256": sha}
	return s.finishMutation(m, sess, "coordination.release_submit", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}

// ---- checkpoint_emit -----------------------------------------------------

// checkpointEmit characterizes a Cockpit workspace strictly from the durable
// controller projection (workspace and pane identity rows). It reads no tmux
// state and no pane output, so a checkpoint can never encode terminal text.
func (s *Service) checkpointEmit(sess Session, params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID     string `json:"workstreamId"`
		ExpectedRevision int64  `json:"expectedRevision"`
		IdempotencyKey   string `json:"idempotencyKey"`
		WorkspaceRef     string `json:"workspaceRef"`
		Reason           string `json:"reason"`
	}
	if err := strictDecode(params, &p); err != nil || p.WorkspaceRef == "" || len(p.WorkspaceRef) > maxShortString {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	if err := boundedString(p.Reason, maxLongString, "reason"); err != nil {
		return nil, err
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s\x00%s", p.WorkstreamID, p.ExpectedRevision, p.WorkspaceRef, sha256Hex([]byte(p.Reason)))
	m, aux, err := s.beginMutation(sess, "coordination.checkpoint_emit", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleOrchestrator)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var exists int
	if err = m.tx.QueryRow("SELECT count(*) FROM workspaces WHERE ref=?", p.WorkspaceRef).Scan(&exists); err != nil {
		return fail(err)
	}
	if exists == 0 {
		return fail(cerr("TARGET_NOT_FOUND", "workspace not found in durable projection"))
	}
	rows, err := m.tx.Query("SELECT ref,generation,version,badge,fenced FROM panes WHERE workspace_ref=? ORDER BY ref", p.WorkspaceRef)
	if err != nil {
		return fail(err)
	}
	var panes []CheckpointPane
	for rows.Next() {
		var cp CheckpointPane
		if err = rows.Scan(&cp.PaneRef, &cp.Generation, &cp.Version, &cp.Badge, &cp.Fenced); err != nil {
			_ = rows.Close()
			return fail(err)
		}
		panes = append(panes, cp)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return fail(err)
	}
	_ = rows.Close()
	if len(panes) > maxListItems {
		return fail(cerr("CAPABILITY_ABSENT", "workspace exceeds checkpoint pane bound"))
	}
	cp := WorkspaceCheckpoint{
		SchemaVersion: "coord.workspace-checkpoint.v0", RecordType: "workspace-checkpoint",
		WorkstreamID: p.WorkstreamID, CreatedAt: m.at.UTC().Format(time.RFC3339),
		CreatedByRole: m.role, WorkspaceRef: p.WorkspaceRef, Reason: p.Reason, Panes: panes,
	}
	cpBytes, _ := json.Marshal(cp)
	ref, sha, err := s.publishRecord(m, "workspace-checkpoint", "", 0, m.role, cpBytes, "")
	if err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "checkpoint.emitted", "", sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"workspaceRef": p.WorkspaceRef, "recordRef": ref, "checkpointSha256": sha, "paneCount": len(panes)}
	return s.finishMutation(m, sess, "coordination.checkpoint_emit", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}
