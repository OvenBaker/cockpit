package coord

import (
	"database/sql"
	"fmt"
)

// Migrate applies the forward-only coordination schema on top of the pane
// controller's version-1 schema. It shares the controller's single SQLite
// database (WAL, FULL synchronous, foreign keys) so every coordination
// transition, event, projection bump, and idempotency result commits in one
// transaction with the artifact metadata it references.
func Migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return fmt.Errorf("store schema %d is newer than supported version 2", version)
	}
	if version == 2 {
		return nil
	}
	if version != 1 {
		return fmt.Errorf("coordination migration requires base schema version 1, found %d", version)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS coord_workstreams(
  id TEXT PRIMARY KEY,
  revision INTEGER NOT NULL,
  contract_sha TEXT NOT NULL,
  repository TEXT NOT NULL,
  created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS coord_roles(
  workstream_id TEXT NOT NULL REFERENCES coord_workstreams(id),
  role TEXT NOT NULL,
  client_id TEXT NOT NULL,
  PRIMARY KEY(workstream_id, role),
  UNIQUE(workstream_id, client_id));
CREATE TABLE IF NOT EXISTS coord_artifacts(
  sha256 TEXT PRIMARY KEY,
  bytes INTEGER NOT NULL,
  stored_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS coord_records(
  ref TEXT PRIMARY KEY,
  workstream_id TEXT NOT NULL REFERENCES coord_workstreams(id),
  record_type TEXT NOT NULL,
  task_id TEXT NOT NULL DEFAULT '',
  task_revision INTEGER NOT NULL DEFAULT 0,
  sha256 TEXT NOT NULL REFERENCES coord_artifacts(sha256),
  created_at INTEGER NOT NULL,
  created_by_role TEXT NOT NULL,
  predecessor_sha TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS coord_tasks(
  workstream_id TEXT NOT NULL REFERENCES coord_workstreams(id),
  task_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  status TEXT NOT NULL,
  assignment_sha TEXT NOT NULL,
  plan_sha TEXT NOT NULL,
  base_sha TEXT NOT NULL,
  seeded_dep_sha TEXT NOT NULL DEFAULT '',
  worktree TEXT NOT NULL,
  branch TEXT NOT NULL,
  lease_id TEXT NOT NULL,
  head_sha TEXT NOT NULL DEFAULT '',
  handoff_sha TEXT NOT NULL DEFAULT '',
  review_request_sha TEXT NOT NULL DEFAULT '',
  review_result_sha TEXT NOT NULL DEFAULT '',
  verdict TEXT NOT NULL DEFAULT '',
  acceptance_sha TEXT NOT NULL DEFAULT '',
  release_sha TEXT NOT NULL DEFAULT '',
  correction_of_revision INTEGER,
  PRIMARY KEY(workstream_id, task_id, revision));
CREATE TABLE IF NOT EXISTS coord_leases(
  workstream_id TEXT NOT NULL REFERENCES coord_workstreams(id),
  lease_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  task_revision INTEGER NOT NULL,
  holder_role TEXT NOT NULL,
  holder_client TEXT NOT NULL,
  scope TEXT NOT NULL,
  status TEXT NOT NULL,
  transfer_sha TEXT NOT NULL DEFAULT '',
  expires_at INTEGER,
  acquired_at INTEGER NOT NULL,
  released_at INTEGER,
  PRIMARY KEY(workstream_id, lease_id));
CREATE UNIQUE INDEX IF NOT EXISTS coord_one_active_lease
  ON coord_leases(workstream_id) WHERE status='active';
CREATE TABLE IF NOT EXISTS coord_deliveries(
  workstream_id TEXT NOT NULL REFERENCES coord_workstreams(id),
  request_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  task_revision INTEGER NOT NULL,
  artifact_sha TEXT NOT NULL,
  prompt_path TEXT NOT NULL,
  prompt_sha TEXT NOT NULL,
  prompt_bytes INTEGER NOT NULL,
  status TEXT NOT NULL,
  pane_id TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  acknowledged_at INTEGER,
  PRIMARY KEY(workstream_id, request_id));
CREATE TABLE IF NOT EXISTS coord_events(
  workstream_id TEXT NOT NULL REFERENCES coord_workstreams(id),
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  task_id TEXT NOT NULL DEFAULT '',
  record_sha TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL,
  PRIMARY KEY(workstream_id, seq));
CREATE TABLE IF NOT EXISTS coord_idempotency(
  caller TEXT NOT NULL,
  method TEXT NOT NULL,
  idem_key TEXT NOT NULL,
  intent_sha TEXT NOT NULL,
  result TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(caller, method, idem_key));
PRAGMA user_version=2;`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
