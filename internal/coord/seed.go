package coord

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Provider-native task pointer delivery.
//
// The controller writes a private, bounded prompt file containing only a
// typed task-pointer envelope, then invokes the shared seeded first-turn
// capability through the pinned four-flag interface
// (--request-id --initial-prompt-file --initial-prompt-sha256
// --initial-prompt-bytes). There is no send-keys or shell-injection path
// here: the launcher is an external pinned producer, the prompt reaches the
// provider as its positional first-turn input, and acknowledgement is a
// structured controller record bound by request id and artifact hash.
// Terminal display, copy mode, and scrolling are irrelevant to delivery.

// Launcher is the narrow seam onto the seeded first-turn capability.
type Launcher interface {
	Launch(req LaunchRequest) (paneID string, err error)
}

type LaunchRequest struct {
	Cwd          string
	Name         string
	RequestID    string
	PromptFile   string
	PromptSha256 string
	PromptBytes  int64
}

// LaunchError distinguishes terminal seed failures (recorded durably) from
// transient environment failures (delivery stays redeliverable).
type LaunchError struct {
	Terminal bool
	Code     string
	Detail   string
}

func (e *LaunchError) Error() string { return e.Code + ": " + e.Detail }

// ExecLauncher invokes the pinned external producer (cockpit-spawn's seeded
// mode). Exit codes follow that contract: 0 ok, 2 validation refused,
// 4 terminal seed failure, 5 changed-material conflict; anything else is
// treated as transient.
type ExecLauncher struct{ Path string }

func (l ExecLauncher) Launch(req LaunchRequest) (string, error) {
	cmd := exec.Command(l.Path,
		"--cwd", req.Cwd,
		"--name", req.Name,
		"--request-id", req.RequestID,
		"--initial-prompt-file", req.PromptFile,
		"--initial-prompt-sha256", req.PromptSha256,
		"--initial-prompt-bytes", fmt.Sprint(req.PromptBytes),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		pane := strings.TrimSpace(stdout.String())
		if pane == "" {
			return "", &LaunchError{Terminal: false, Code: "seed-launcher-no-pane", Detail: "launcher returned no pane id"}
		}
		return pane, nil
	}
	detail := strings.TrimSpace(stderr.String())
	if len(detail) > maxShortString {
		detail = detail[:maxShortString]
	}
	if exit, ok := err.(*exec.ExitError); ok {
		switch exit.ExitCode() {
		case 2:
			return "", &LaunchError{Terminal: true, Code: "seed-validation-refused", Detail: detail}
		case 4:
			return "", &LaunchError{Terminal: true, Code: "seed-terminal-failure", Detail: detail}
		case 5:
			return "", &LaunchError{Terminal: true, Code: "seed-material-conflict", Detail: detail}
		}
	}
	return "", &LaunchError{Terminal: false, Code: "seed-launcher-unavailable", Detail: detail}
}

func pointerEnvelope(workstreamID, taskID string, revision int64, requestID, artifactPath, artifactSha string) []byte {
	return fmt.Appendf(nil, "coord.task-pointer.v0 workstreamId=%s taskId=%s revision=%d requestId=%s artifactPath=%s artifactHash=%s\n",
		workstreamID, taskID, revision, requestID, artifactPath, artifactSha)
}

func (s *Service) seedPromptPath(requestID string) string {
	return filepath.Join(s.seedDir(), requestID+".prompt")
}

// writePromptFile creates the private prompt file with no-replace semantics.
// A pre-existing file must byte-match the expected envelope; anything else
// fails closed.
func (s *Service) writePromptFile(path string, content []byte) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return cerr("CONTROLLER_NOT_READY", "seed prompt path is not a private regular file")
		}
		existing, rerr := os.ReadFile(path)
		if rerr != nil || !bytes.Equal(existing, content) {
			return cerr("CONFLICT_MATERIAL_STATE", "seed prompt file exists with different material")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".seed-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = os.Chmod(tmpName, 0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Link(tmpName, path); err != nil {
		if os.IsExist(err) {
			existing, rerr := os.ReadFile(path)
			if rerr == nil && bytes.Equal(existing, content) {
				return nil
			}
			return cerr("CONFLICT_MATERIAL_STATE", "seed prompt file exists with different material")
		}
		return err
	}
	return nil
}

// verifyPromptFile re-verifies the staged prompt immediately before launch:
// private regular non-symlink file whose bytes match the recorded digest and
// length. Any drift fails closed without launching.
func verifyPromptFile(path, wantSha string, wantBytes int64) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return cerr("CONTROLLER_NOT_READY", "seed prompt file is missing or not a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return cerr("CONTROLLER_NOT_READY", "seed prompt file is not private")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(b)) != wantBytes || sha256Hex(b) != wantSha {
		return cerr("CONFLICT_MATERIAL_STATE", "seed prompt file drifted from its recorded material")
	}
	return nil
}

// ---- task_deliver --------------------------------------------------------

func (s *Service) taskDeliver(sess Session, params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID     string `json:"workstreamId"`
		ExpectedRevision int64  `json:"expectedRevision"`
		IdempotencyKey   string `json:"idempotencyKey"`
		TaskID           string `json:"taskId"`
		TaskRevision     int64  `json:"taskRevision"`
		RequestID        string `json:"requestId"`
	}
	if err := strictDecode(params, &p); err != nil || !taskIDRE.MatchString(p.TaskID) || p.TaskRevision < 0 || !requestIDRE.MatchString(p.RequestID) {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()

	// Phase A: durable reservation before any launch side effect.
	intent := fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%s", p.WorkstreamID, p.ExpectedRevision, p.TaskID, p.TaskRevision, p.RequestID)
	m, aux, err := s.beginMutation(sess, "coordination.task_deliver", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleOrchestrator)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var taskStatus, assignmentSha, worktree string
	err = m.tx.QueryRow("SELECT status,assignment_sha,worktree FROM coord_tasks WHERE workstream_id=? AND task_id=? AND revision=?", p.WorkstreamID, p.TaskID, p.TaskRevision).Scan(&taskStatus, &assignmentSha, &worktree)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "task not found"))
	}
	if err != nil {
		return fail(err)
	}
	if taskStatus != StatusPublished {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "delivery requires a published task, task is "+taskStatus))
	}
	promptPath := s.seedPromptPath(p.RequestID)
	envelope := pointerEnvelope(p.WorkstreamID, p.TaskID, p.TaskRevision, p.RequestID, s.artifactPath(assignmentSha), assignmentSha)
	promptSha := sha256Hex(envelope)
	promptBytes := int64(len(envelope))

	var status, rowTask, rowArtifact, rowPromptSha, paneID string
	var rowRev, rowBytes int64
	err = m.tx.QueryRow("SELECT status,task_id,task_revision,artifact_sha,prompt_sha,prompt_bytes,pane_id FROM coord_deliveries WHERE workstream_id=? AND request_id=?", p.WorkstreamID, p.RequestID).Scan(&status, &rowTask, &rowRev, &rowArtifact, &rowPromptSha, &rowBytes, &paneID)
	switch {
	case err == sql.ErrNoRows:
		if err = s.writePromptFile(promptPath, envelope); err != nil {
			return fail(err)
		}
		if _, err = m.tx.Exec("INSERT INTO coord_deliveries(workstream_id,request_id,task_id,task_revision,artifact_sha,prompt_path,prompt_sha,prompt_bytes,status,created_at) VALUES(?,?,?,?,?,?,?,?,'prepared',?)",
			p.WorkstreamID, p.RequestID, p.TaskID, p.TaskRevision, assignmentSha, promptPath, promptSha, promptBytes, m.at.Unix()); err != nil {
			return fail(err)
		}
		seq, aerr := s.addEvent(m, "task.delivery-prepared", p.TaskID, assignmentSha)
		if aerr != nil {
			return fail(aerr)
		}
		if _, err = m.tx.Exec("UPDATE coord_workstreams SET revision=? WHERE id=?", m.revision, p.WorkstreamID); err != nil {
			return fail(err)
		}
		// The reservation is the durable mutation this idempotency key owns.
		// A reused key with different intent now conflicts before any effect;
		// the launched outcome updates this result in the settlement phase.
		reserved, _ := json.Marshal(map[string]any{"requestId": p.RequestID, "taskId": p.TaskID, "taskRevision": p.TaskRevision, "status": "prepared", "workstreamRevision": m.revision, "eventSeq": seq})
		if _, err = m.tx.Exec("INSERT INTO coord_idempotency(caller,method,idem_key,intent_sha,result,created_at) VALUES(?,?,?,?,?,?)", sess.Caller, "coordination.task_deliver", p.IdempotencyKey, aux["intentSha"].(string), string(reserved), m.at.Unix()); err != nil {
			return fail(err)
		}
		if err = m.tx.Commit(); err != nil {
			return nil, err
		}
		s.notify(p.WorkstreamID, seq)
	case err != nil:
		return fail(err)
	default:
		// Same request id: the material must bind exactly; drift conflicts
		// loudly before any side effect.
		if rowTask != p.TaskID || rowRev != p.TaskRevision || rowArtifact != assignmentSha || rowPromptSha != promptSha || rowBytes != promptBytes {
			return fail(cerr("CONFLICT_MATERIAL_STATE", "seed request was already reserved with different material"))
		}
		_ = m.tx.Rollback()
		switch status {
		case "acknowledged":
			return map[string]any{"requestId": p.RequestID, "taskId": p.TaskID, "taskRevision": p.TaskRevision, "status": status, "paneId": paneID, "promptSha256": promptSha, "promptBytes": promptBytes}, nil
		case "failed":
			return nil, cerr("CONFLICT_MATERIAL_STATE", "seed request already failed terminally")
		}
		// prepared or launched: fall through and (re)invoke the idempotent
		// launcher, which reconciles to the one canonical pane.
	}

	// Phase B: fail-closed verification and provider-native launch. This is
	// outside any transaction; the durable reservation already exists.
	if s.launcher == nil {
		return nil, cerr("CAPABILITY_ABSENT", "no seeded launcher is configured")
	}
	if err = verifyPromptFile(promptPath, promptSha, promptBytes); err != nil {
		return nil, err
	}
	if _, err = s.readArtifact(s.db, assignmentSha); err != nil {
		return nil, err
	}
	pane, launchErr := s.launcher.Launch(LaunchRequest{
		Cwd: worktree, Name: p.TaskID, RequestID: p.RequestID,
		PromptFile: promptPath, PromptSha256: promptSha, PromptBytes: promptBytes,
	})

	// Phase C: record the durable outcome.
	if launchErr != nil {
		le, ok := launchErr.(*LaunchError)
		if !ok || !le.Terminal {
			// Transient: the reservation stays redeliverable.
			return nil, cerr("CONTROLLER_NOT_READY", "seed launcher unavailable: "+launchErr.Error())
		}
		if err = s.deliveryOutcome(p.WorkstreamID, p.RequestID, p.TaskID, "failed", "", le.Code, "task.delivery-failed", sess.Caller, p.IdempotencyKey, nil); err != nil {
			return nil, err
		}
		return nil, cerr("CONFLICT_MATERIAL_STATE", "seed launch failed terminally: "+le.Code)
	}
	result := map[string]any{"requestId": p.RequestID, "taskId": p.TaskID, "taskRevision": p.TaskRevision, "status": "launched", "paneId": pane, "promptSha256": promptSha, "promptBytes": promptBytes}
	if err = s.deliveryOutcome(p.WorkstreamID, p.RequestID, p.TaskID, "launched", pane, "", "task.delivery-launched", sess.Caller, p.IdempotencyKey, result); err != nil {
		return nil, err
	}
	return result, nil
}

// deliveryOutcome commits the post-launch delivery state, event, and
// projection bump in one transaction. When finalResult is non-nil the
// caller's idempotency record is settled to the launched outcome so a
// same-key replay returns it.
func (s *Service) deliveryOutcome(ws, requestID, taskID, status, paneID, errCode, eventKind, caller, idemKey string, finalResult map[string]any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	var current string
	if err = tx.QueryRow("SELECT status FROM coord_deliveries WHERE workstream_id=? AND request_id=?", ws, requestID).Scan(&current); err != nil {
		return rollback(err)
	}
	if current == "acknowledged" || current == "failed" {
		// Another path already settled this delivery; keep the settled state.
		_ = tx.Rollback()
		return nil
	}
	if _, err = tx.Exec("UPDATE coord_deliveries SET status=?,pane_id=?,error=? WHERE workstream_id=? AND request_id=?", status, paneID, errCode, ws, requestID); err != nil {
		return rollback(err)
	}
	var seq int64
	if err = tx.QueryRow("SELECT COALESCE(MAX(seq),0)+1 FROM coord_events WHERE workstream_id=?", ws).Scan(&seq); err != nil {
		return rollback(err)
	}
	if _, err = tx.Exec("INSERT INTO coord_events(workstream_id,seq,kind,task_id,record_sha,at) VALUES(?,?,?,?,?,?)", ws, seq, eventKind, taskID, "", s.now().Unix()); err != nil {
		return rollback(err)
	}
	if _, err = tx.Exec("UPDATE coord_workstreams SET revision=revision+1 WHERE id=?", ws); err != nil {
		return rollback(err)
	}
	if finalResult != nil {
		rb, merr := json.Marshal(finalResult)
		if merr != nil {
			return rollback(merr)
		}
		if _, err = tx.Exec("UPDATE coord_idempotency SET result=? WHERE caller=? AND method='coordination.task_deliver' AND idem_key=?", string(rb), caller, idemKey); err != nil {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.notify(ws, seq)
	return nil
}

// ---- task_acknowledge ----------------------------------------------------

// taskAcknowledge records structured receipt of a delivered pointer. It is
// bound by request id, task identity, and the exact assignment artifact hash;
// no terminal observation participates.
func (s *Service) taskAcknowledge(sess Session, params json.RawMessage) (any, error) {
	var p struct {
		WorkstreamID     string `json:"workstreamId"`
		ExpectedRevision int64  `json:"expectedRevision"`
		IdempotencyKey   string `json:"idempotencyKey"`
		RequestID        string `json:"requestId"`
		TaskID           string `json:"taskId"`
		TaskRevision     int64  `json:"taskRevision"`
		ArtifactSha256   string `json:"artifactSha256"`
	}
	if err := strictDecode(params, &p); err != nil || !taskIDRE.MatchString(p.TaskID) || p.TaskRevision < 0 || !requestIDRE.MatchString(p.RequestID) || !sha256RE.MatchString(p.ArtifactSha256) {
		return nil, cerr("INVALID_REQUEST", "invalid params")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	intent := fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%s\x00%s", p.WorkstreamID, p.ExpectedRevision, p.TaskID, p.TaskRevision, p.RequestID, p.ArtifactSha256)
	m, aux, err := s.beginMutation(sess, "coordination.task_acknowledge", p.WorkstreamID, p.ExpectedRevision, p.IdempotencyKey, intent, RoleBuilder)
	if err == errReplayed {
		return aux, nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) { _ = m.tx.Rollback(); return nil, e }
	var status, rowTask, rowArtifact string
	var rowRev int64
	err = m.tx.QueryRow("SELECT status,task_id,task_revision,artifact_sha FROM coord_deliveries WHERE workstream_id=? AND request_id=?", p.WorkstreamID, p.RequestID).Scan(&status, &rowTask, &rowRev, &rowArtifact)
	if err == sql.ErrNoRows {
		return fail(cerr("TARGET_NOT_FOUND", "no delivery with this request id"))
	}
	if err != nil {
		return fail(err)
	}
	if rowTask != p.TaskID || rowRev != p.TaskRevision || rowArtifact != p.ArtifactSha256 {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "acknowledgement does not bind the delivered task and artifact hash"))
	}
	if status == "failed" {
		return fail(cerr("CONFLICT_MATERIAL_STATE", "delivery already failed terminally"))
	}
	if status == "acknowledged" {
		_ = m.tx.Rollback()
		return map[string]any{"requestId": p.RequestID, "taskId": p.TaskID, "taskRevision": p.TaskRevision, "status": "acknowledged", "alreadyAcknowledged": true}, nil
	}
	ack := TaskAcknowledgement{
		SchemaVersion: "coord.task-acknowledgement.v0", RecordType: "task-acknowledgement",
		WorkstreamID: p.WorkstreamID, TaskID: p.TaskID, TaskRevision: p.TaskRevision,
		CreatedAt: m.at.UTC().Format(time.RFC3339), CreatedByRole: RoleBuilder,
		RequestID: p.RequestID, ArtifactSha256: p.ArtifactSha256, AcknowledgedBy: sess.Caller,
	}
	ackBytes, _ := json.Marshal(ack)
	ref, sha, err := s.publishRecord(m, "task-acknowledgement", p.TaskID, p.TaskRevision, RoleBuilder, ackBytes, p.ArtifactSha256)
	if err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_deliveries SET status='acknowledged',acknowledged_at=? WHERE workstream_id=? AND request_id=?", m.at.Unix(), p.WorkstreamID, p.RequestID); err != nil {
		return fail(err)
	}
	if _, err = m.tx.Exec("UPDATE coord_tasks SET status=? WHERE workstream_id=? AND task_id=? AND revision=? AND status=?", StatusAcknowledged, p.WorkstreamID, p.TaskID, p.TaskRevision, StatusPublished); err != nil {
		return fail(err)
	}
	seq, err := s.addEvent(m, "task.acknowledged", p.TaskID, sha)
	if err != nil {
		return fail(err)
	}
	result := map[string]any{"requestId": p.RequestID, "taskId": p.TaskID, "taskRevision": p.TaskRevision, "status": "acknowledged", "recordRef": ref, "acknowledgementSha256": sha}
	return s.finishMutation(m, sess, "coordination.task_acknowledge", p.IdempotencyKey, aux["intentSha"].(string), result, seq)
}
