package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Session is the transport-authenticated caller identity. Fixed reports
// whether the client identity was pinned by the credential grant itself;
// coordination mutations refuse identities a client merely claimed, because
// durable role assignments bind to credential-pinned client ids.
type Session struct {
	Caller  string
	Fixed   bool
	Profile string
}

// Service is the coordination transition authority. It shares the resident
// controller's single SQLite database and commits every transition, event,
// projection bump, and idempotency result in one transaction.
type Service struct {
	db       *sql.DB
	root     string
	launcher Launcher

	mutateMu sync.Mutex
	waitMu   sync.Mutex
	waiters  map[int]*waiter
	waiterID int
	now      func() time.Time
}

type waiter struct {
	workstream string
	after      int64
	ch         chan int64
}

// New wires the coordination service onto the controller's database and
// private runtime root. launcher may be nil; delivery then fails closed.
func New(db *sql.DB, root string, launcher Launcher) (*Service, error) {
	if err := Migrate(db); err != nil {
		return nil, err
	}
	s := &Service{db: db, root: root, launcher: launcher, waiters: map[int]*waiter{}, now: time.Now}
	if err := s.Reconcile(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dispatch routes one coordination.* method. Capability gating happened at
// the transport registry; role and state authority are enforced here.
func (s *Service) Dispatch(ctx context.Context, sess Session, method string, params json.RawMessage) (any, error) {
	switch method {
	case "coordination.workstream_create":
		return s.workstreamCreate(sess, params)
	case "coordination.task_publish":
		return s.taskPublish(sess, params)
	case "coordination.task_deliver":
		return s.taskDeliver(sess, params)
	case "coordination.task_acknowledge":
		return s.taskAcknowledge(sess, params)
	case "coordination.task_claim":
		return s.taskClaim(sess, params)
	case "coordination.artifact_publish":
		return s.artifactPublish(sess, params)
	case "coordination.artifact_read":
		return s.artifactRead(params)
	case "coordination.handoff_submit":
		return s.handoffSubmit(sess, params)
	case "coordination.review_request":
		return s.reviewRequest(sess, params)
	case "coordination.review_submit":
		return s.reviewSubmit(sess, params)
	case "coordination.lease_transfer":
		return s.leaseTransfer(sess, params)
	case "coordination.lease_return":
		return s.leaseReturn(sess, params)
	case "coordination.acceptance_submit":
		return s.acceptanceSubmit(sess, params)
	case "coordination.release_submit":
		return s.releaseSubmit(sess, params)
	case "coordination.checkpoint_emit":
		return s.checkpointEmit(sess, params)
	case "coordination.status_get":
		return s.statusGet(params)
	case "coordination.events_list":
		return s.eventsList(params)
	case "coordination.wait":
		return s.wait(ctx, params)
	}
	return nil, cerr("INVALID_REQUEST", "unknown coordination method")
}

// ---- mutation plumbing ---------------------------------------------------

// mutation is the common envelope for record-carrying mutations.
type mutation struct {
	WorkstreamID     string          `json:"workstreamId"`
	ExpectedRevision int64           `json:"expectedRevision"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	Record           json.RawMessage `json:"record"`
}

// mutState carries the per-mutation transactional context.
type mutState struct {
	tx       *sql.Tx
	ws       string
	caller   string
	role     string
	revision int64 // revision after this mutation commits
	at       time.Time
}

var errReplayed = fmt.Errorf("idempotent replay")

// beginMutation validates the envelope, opens the transaction, resolves the
// caller's server-side role, checks idempotency and the projection CAS, and
// returns the mutation state. On errReplayed the prior result is returned in
// replay. The caller must Commit or Rollback the transaction.
func (s *Service) beginMutation(sess Session, method, ws string, expectedRevision int64, idemKey, intent string, requireRole string) (*mutState, map[string]any, error) {
	if !sess.Fixed {
		return nil, nil, cerr("PERMISSION_DENIED", "coordination mutations require a credential-pinned client identity")
	}
	if !workstreamIDRE.MatchString(ws) {
		return nil, nil, cerr("INVALID_REQUEST", "invalid workstreamId")
	}
	now := s.now()
	if err := validIdempotencyKey(idemKey, now); err != nil {
		return nil, nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	fail := func(e error) (*mutState, map[string]any, error) {
		_ = tx.Rollback()
		return nil, nil, e
	}
	var revision int64
	err = tx.QueryRow("SELECT revision FROM coord_workstreams WHERE id=?", ws).Scan(&revision)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "workstream not found"))
	}
	if err != nil {
		return fail(err)
	}
	intentSha := sha256Hex([]byte(method + "\x00" + intent))
	var priorIntent, priorResult string
	err = tx.QueryRow("SELECT intent_sha,result FROM coord_idempotency WHERE caller=? AND method=? AND idem_key=?", sess.Caller, method, idemKey).Scan(&priorIntent, &priorResult)
	if err == nil {
		_ = tx.Rollback()
		if priorIntent != intentSha {
			return nil, nil, cerr("IDEMPOTENCY_CONFLICT", "idempotency key has a different intent")
		}
		var prior map[string]any
		_ = json.Unmarshal([]byte(priorResult), &prior)
		if prior == nil {
			prior = map[string]any{}
		}
		prior["replayed"] = true
		return nil, prior, errReplayed
	}
	if err != sql.ErrNoRows {
		return fail(err)
	}
	if expectedRevision != revision {
		return fail(cerr("CONFLICT_VERSION", fmt.Sprintf("workstream revision is %d, expected %d", revision, expectedRevision)))
	}
	role, err := s.roleOf(tx, ws, sess.Caller)
	if err != nil {
		return fail(err)
	}
	if requireRole != "" && role != requireRole {
		return fail(cerr("PERMISSION_DENIED", "operation requires the "+requireRole+" role"))
	}
	m := &mutState{tx: tx, ws: ws, caller: sess.Caller, role: role, revision: revision + 1, at: now}
	return m, map[string]any{"intentSha": intentSha}, nil
}

func (s *Service) roleOf(tx *sql.Tx, ws, caller string) (string, error) {
	var role string
	err := tx.QueryRow("SELECT role FROM coord_roles WHERE workstream_id=? AND client_id=?", ws, caller).Scan(&role)
	if err == sql.ErrNoRows {
		return "", cerr("PERMISSION_DENIED", "caller holds no role in this workstream")
	}
	return role, err
}

// publishRecord stages the canonical bytes durably and records the immutable
// artifact + record metadata inside the mutation transaction.
func (s *Service) publishRecord(m *mutState, recordType, taskID string, taskRev int64, byRole string, canon []byte, predecessorSha string) (string, string, error) {
	sha, err := s.stageArtifact(canon)
	if err != nil {
		return "", "", err
	}
	var existing int64
	err = m.tx.QueryRow("SELECT bytes FROM coord_artifacts WHERE sha256=?", sha).Scan(&existing)
	if err == sql.ErrNoRows {
		if _, err = m.tx.Exec("INSERT INTO coord_artifacts(sha256,bytes,stored_at) VALUES(?,?,?)", sha, len(canon), m.at.Unix()); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	} else if existing != int64(len(canon)) {
		return "", "", cerr("CONTROLLER_NOT_READY", "artifact metadata digest collision")
	}
	ref := newRef("crd_")
	_, err = m.tx.Exec("INSERT INTO coord_records(ref,workstream_id,record_type,task_id,task_revision,sha256,created_at,created_by_role,predecessor_sha) VALUES(?,?,?,?,?,?,?,?,?)",
		ref, m.ws, recordType, taskID, taskRev, sha, m.at.Unix(), byRole, predecessorSha)
	if err != nil {
		return "", "", err
	}
	return ref, sha, nil
}

func (s *Service) addEvent(m *mutState, kind, taskID, recordSha string) (int64, error) {
	var seq int64
	if err := m.tx.QueryRow("SELECT COALESCE(MAX(seq),0)+1 FROM coord_events WHERE workstream_id=?", m.ws).Scan(&seq); err != nil {
		return 0, err
	}
	_, err := m.tx.Exec("INSERT INTO coord_events(workstream_id,seq,kind,task_id,record_sha,at) VALUES(?,?,?,?,?,?)", m.ws, seq, kind, taskID, recordSha, m.at.Unix())
	return seq, err
}

// finishMutation bumps the projection revision, stores the idempotency
// result, commits, and wakes waiters. result is mutated to include the new
// revision before storage so replays observe identical bytes.
func (s *Service) finishMutation(m *mutState, sess Session, method, idemKey, intentSha string, result map[string]any, lastSeq int64) (map[string]any, error) {
	if _, err := m.tx.Exec("UPDATE coord_workstreams SET revision=? WHERE id=?", m.revision, m.ws); err != nil {
		_ = m.tx.Rollback()
		return nil, err
	}
	result["workstreamRevision"] = m.revision
	result["eventSeq"] = lastSeq
	rb, err := json.Marshal(result)
	if err != nil {
		_ = m.tx.Rollback()
		return nil, err
	}
	if _, err = m.tx.Exec("INSERT INTO coord_idempotency(caller,method,idem_key,intent_sha,result,created_at) VALUES(?,?,?,?,?,?)", sess.Caller, method, idemKey, intentSha, string(rb), m.at.Unix()); err != nil {
		_ = m.tx.Rollback()
		return nil, err
	}
	if err = m.tx.Commit(); err != nil {
		return nil, err
	}
	s.notify(m.ws, lastSeq)
	return result, nil
}

// ---- read operations -----------------------------------------------------

func (s *Service) artifactRead(params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID string `json:"workstreamId"`
		Sha256       string `json:"sha256"`
	}
	if err := strictDecode(params, &p); err != nil || !workstreamIDRE.MatchString(p.WorkstreamID) {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	var recordType string
	err := s.db.QueryRow("SELECT record_type FROM coord_records WHERE workstream_id=? AND sha256=? LIMIT 1", p.WorkstreamID, p.Sha256).Scan(&recordType)
	if err == sql.ErrNoRows {
		return nil, cerr("TARGET_NOT_FOUND", "record not found in workstream")
	}
	if err != nil {
		return nil, err
	}
	b, err := s.readArtifact(s.db, p.Sha256)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workstreamId": p.WorkstreamID, "sha256": p.Sha256, "bytes": len(b), "recordType": recordType, "record": json.RawMessage(b)}, nil
}

func (s *Service) statusGet(params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID string `json:"workstreamId"`
	}
	if err := strictDecode(params, &p); err != nil || !workstreamIDRE.MatchString(p.WorkstreamID) {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	var revision int64
	var contractSha, repository string
	err := s.db.QueryRow("SELECT revision,contract_sha,repository FROM coord_workstreams WHERE id=?", p.WorkstreamID).Scan(&revision, &contractSha, &repository)
	if err == sql.ErrNoRows {
		return nil, cerr("TARGET_NOT_FOUND", "workstream not found")
	}
	if err != nil {
		return nil, err
	}
	var eventSeq int64
	if err = s.db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM coord_events WHERE workstream_id=?", p.WorkstreamID).Scan(&eventSeq); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT task_id,revision,status,assignment_sha,head_sha,handoff_sha,review_request_sha,review_result_sha,verdict,acceptance_sha,release_sha,policy_version,brief_sha
		FROM coord_tasks WHERE workstream_id=? ORDER BY task_id,revision`, p.WorkstreamID)
	if err != nil {
		return nil, err
	}
	tasks := []any{}
	for rows.Next() {
		var id, status, assignment, head, handoff, reviewReq, reviewRes, verdict, acceptance, release, policyVersion, briefSha string
		var rev int64
		if err = rows.Scan(&id, &rev, &status, &assignment, &head, &handoff, &reviewReq, &reviewRes, &verdict, &acceptance, &release, &policyVersion, &briefSha); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tasks = append(tasks, map[string]any{
			"taskId": id, "revision": rev, "status": status, "assignmentSha256": assignment,
			"headSha": head, "handoffSha256": handoff, "reviewRequestSha256": reviewReq,
			"reviewResultSha256": reviewRes, "verdict": verdict, "acceptanceSha256": acceptance,
			"releaseSha256": release, "policyVersion": policyVersion, "briefPackageSha256": briefSha,
		})
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	var lease map[string]any
	var leaseID, taskID, holderRole, holderClient, scope string
	var taskRev int64
	err = s.db.QueryRow("SELECT lease_id,task_id,task_revision,holder_role,holder_client,scope FROM coord_leases WHERE workstream_id=? AND status='active'", p.WorkstreamID).Scan(&leaseID, &taskID, &taskRev, &holderRole, &holderClient, &scope)
	if err == nil {
		lease = map[string]any{"leaseId": leaseID, "taskId": taskID, "taskRevision": taskRev, "holderRole": holderRole, "holderClient": holderClient, "scope": scope}
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	drows, err := s.db.Query("SELECT request_id,task_id,task_revision,status,pane_id FROM coord_deliveries WHERE workstream_id=? ORDER BY created_at DESC LIMIT 16", p.WorkstreamID)
	if err != nil {
		return nil, err
	}
	deliveries := []any{}
	for drows.Next() {
		var reqID, tid, status, paneID string
		var rev int64
		if err = drows.Scan(&reqID, &tid, &rev, &status, &paneID); err != nil {
			_ = drows.Close()
			return nil, err
		}
		deliveries = append(deliveries, map[string]any{"requestId": reqID, "taskId": tid, "taskRevision": rev, "status": status, "paneId": paneID})
	}
	if err = drows.Err(); err != nil {
		_ = drows.Close()
		return nil, err
	}
	_ = drows.Close()
	return map[string]any{
		"schemaVersion": "coord.status.v0", "workstreamId": p.WorkstreamID, "revision": revision,
		"eventSeq": eventSeq, "contractSha256": contractSha, "repository": repository,
		"tasks": tasks, "activeLease": lease, "deliveries": deliveries,
	}, nil
}

func (s *Service) eventsList(params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID string `json:"workstreamId"`
		AfterSeq     int64  `json:"afterSeq"`
		Limit        int64  `json:"limit"`
	}
	if err := strictDecode(params, &p); err != nil || !workstreamIDRE.MatchString(p.WorkstreamID) || p.AfterSeq < 0 || p.Limit < 0 || p.Limit > maxEventsPage {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	if p.Limit == 0 {
		p.Limit = 100
	}
	var exists int
	if err := s.db.QueryRow("SELECT count(*) FROM coord_workstreams WHERE id=?", p.WorkstreamID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, cerr("TARGET_NOT_FOUND", "workstream not found")
	}
	var current int64
	if err := s.db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM coord_events WHERE workstream_id=?", p.WorkstreamID).Scan(&current); err != nil {
		return nil, err
	}
	rows, err := s.db.Query("SELECT seq,kind,task_id,record_sha,at FROM coord_events WHERE workstream_id=? AND seq>? ORDER BY seq LIMIT ?", p.WorkstreamID, p.AfterSeq, p.Limit)
	if err != nil {
		return nil, err
	}
	events := []any{}
	next := p.AfterSeq
	for rows.Next() {
		var seq, at int64
		var kind, taskID, sha string
		if err = rows.Scan(&seq, &kind, &taskID, &sha, &at); err != nil {
			_ = rows.Close()
			return nil, err
		}
		events = append(events, map[string]any{"schemaVersion": "coord.event.v0", "seq": seq, "kind": kind, "taskId": taskID, "recordSha256": sha, "at": time.Unix(at, 0).UTC().Format(time.RFC3339)})
		next = seq
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	return map[string]any{"workstreamId": p.WorkstreamID, "events": events, "nextAfterSeq": next, "eventSeq": current, "more": next < current}, nil
}

// ---- one-shot bounded wait ----------------------------------------------

func (s *Service) wait(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID string `json:"workstreamId"`
		AfterSeq     int64  `json:"afterSeq"`
		Deadline     string `json:"deadline"`
	}
	if err := strictDecode(params, &p); err != nil || !workstreamIDRE.MatchString(p.WorkstreamID) || p.AfterSeq < 0 {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	dl, err := time.Parse(time.RFC3339, p.Deadline)
	if err != nil || time.Until(dl) > maxWaitDuration {
		return nil, cerr("INVALID_REQUEST", "invalid wait deadline")
	}
	var exists int
	if err := s.db.QueryRow("SELECT count(*) FROM coord_workstreams WHERE id=?", p.WorkstreamID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, cerr("TARGET_NOT_FOUND", "workstream not found")
	}
	s.waitMu.Lock()
	var current int64
	if err := s.db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM coord_events WHERE workstream_id=?", p.WorkstreamID).Scan(&current); err != nil {
		s.waitMu.Unlock()
		return nil, err
	}
	if current > p.AfterSeq {
		s.waitMu.Unlock()
		return map[string]any{"matched": true, "workstreamId": p.WorkstreamID, "eventSeq": current}, nil
	}
	if len(s.waiters) >= maxWaiters {
		s.waitMu.Unlock()
		return nil, cerr("CAPABILITY_ABSENT", "coordination waiter limit reached")
	}
	s.waiterID++
	key := s.waiterID
	w := &waiter{workstream: p.WorkstreamID, after: p.AfterSeq, ch: make(chan int64, 1)}
	s.waiters[key] = w
	s.waitMu.Unlock()
	defer func() {
		s.waitMu.Lock()
		delete(s.waiters, key)
		s.waitMu.Unlock()
	}()
	timer := time.NewTimer(time.Until(dl))
	defer timer.Stop()
	select {
	case seq := <-w.ch:
		return map[string]any{"matched": true, "workstreamId": p.WorkstreamID, "eventSeq": seq}, nil
	case <-timer.C:
		return nil, cerr("DEADLINE_EXCEEDED", "wait deadline exceeded")
	case <-ctx.Done():
		return nil, cerr("CANCELLED", "wait cancelled")
	}
}

// notify wakes waiters after a committed transition. Commit strictly precedes
// notification, so a waiter that read the projection before registering can
// never miss the sequence it is waiting for.
func (s *Service) notify(ws string, seq int64) {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	for key, w := range s.waiters {
		if w.workstream == ws && seq > w.after {
			select {
			case w.ch <- seq:
			default:
			}
			delete(s.waiters, key)
		}
	}
}

// WaiterCount supports leak assertions in tests and health reporting.
func (s *Service) WaiterCount() int {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	return len(s.waiters)
}
