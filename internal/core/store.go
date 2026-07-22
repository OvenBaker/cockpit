package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type store struct {
	db   *sql.DB
	root string
}

func openStore(root string) (*store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if actor := testStoreActor(root); actor != "" {
		f, err := os.OpenFile(filepath.Join(root, "store.trace"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintln(f, actor)
			_ = f.Close()
		}
	}
	db, e := sql.Open("sqlite", filepath.Join(root, "control.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &store{db: db, root: root}
	if e = s.migrate(); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *store) migrate() error {
	if _, err := s.db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL"); err != nil {
		return err
	}
	// Version 1 is the pane-controller schema; versions 2-3 add the
	// coordination domain (owned by internal/coord.Migrate, applied after
	// this base step).
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 3 {
		return fmt.Errorf("store schema %d is newer than supported version 3", version)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if version == 0 {
		_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY,v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workspaces(ref TEXT PRIMARY KEY,window_id TEXT UNIQUE,name TEXT,generation INTEGER,version INTEGER);
CREATE TABLE IF NOT EXISTS panes(ref TEXT PRIMARY KEY,workspace_ref TEXT REFERENCES workspaces(ref),window_id TEXT,pane_id TEXT UNIQUE,generation INTEGER,version INTEGER,badge TEXT NOT NULL DEFAULT '',fenced INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS operations(ref TEXT PRIMARY KEY,caller TEXT,method TEXT,idem_key TEXT,intent TEXT,status TEXT,pane_ref TEXT,badge TEXT,target_version INTEGER,result TEXT,created_at INTEGER, UNIQUE(caller,method,idem_key));
CREATE TABLE IF NOT EXISTS audit(seq INTEGER PRIMARY KEY AUTOINCREMENT,at INTEGER,caller TEXT,method TEXT,pane_ref TEXT,before_digest TEXT,after_digest TEXT);
PRAGMA user_version=1;`)
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var integrity string
	if err = s.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("sqlite integrity_check: %s", integrity)
	}
	return nil
}
func (s *store) close() error { return s.db.Close() }
func (s *store) meta(k string) (string, error) {
	var v string
	e := s.db.QueryRow("SELECT v FROM meta WHERE k=?", k).Scan(&v)
	return v, e
}
func (s *store) setMeta(k, v string) error {
	_, e := s.db.Exec("INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", k, v)
	return e
}
func (s *store) pane(ref string) (pane, error) {
	var p pane
	e := s.db.QueryRow("SELECT ref,workspace_ref,window_id,pane_id,badge,generation,version,fenced FROM panes WHERE ref=?", ref).Scan(&p.Ref, &p.WorkspaceRef, &p.WindowID, &p.PaneID, &p.Badge, &p.Generation, &p.Version, &p.Fenced)
	return p, e
}
func (s *store) panes() ([]pane, error) {
	rows, e := s.db.Query("SELECT ref,workspace_ref,window_id,pane_id,badge,generation,version,fenced FROM panes ORDER BY ref")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []pane
	for rows.Next() {
		var p pane
		if e = rows.Scan(&p.Ref, &p.WorkspaceRef, &p.WindowID, &p.PaneID, &p.Badge, &p.Generation, &p.Version, &p.Fenced); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *store) operation(ref string) (map[string]any, error) {
	var caller, method, key, intent, status, pane, badge, result string
	var created int64
	e := s.db.QueryRow("SELECT caller,method,idem_key,intent,status,pane_ref,badge,result,created_at FROM operations WHERE ref=?", ref).Scan(&caller, &method, &key, &intent, &status, &pane, &badge, &result, &created)
	if e != nil {
		return nil, e
	}
	var r any
	_ = json.Unmarshal([]byte(result), &r)
	return map[string]any{"operationRef": ref, "status": status, "paneRef": pane, "result": r, "createdAt": time.Unix(created, 0).UTC().Format(time.RFC3339)}, nil
}
func intent(p badgeParams) string {
	b, _ := json.Marshal(struct {
		Ref, Badge string
		E          []paneExpectation
	}{p.PaneRef, p.Badge, p.Expectations})
	return string(b)
}
func mustTx(tx *sql.Tx, q string, args ...any) error { _, e := tx.Exec(q, args...); return e }
