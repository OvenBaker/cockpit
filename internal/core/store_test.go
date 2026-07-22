package core

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestStoreV1PragmasVersionAndFutureRefusal(t *testing.T) {
	root := t.TempDir()
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("user_version=%d err=%v", version, err)
	}
	var foreign, busy, syncMode int
	if err = s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreign); err != nil || foreign != 1 {
		t.Fatalf("foreign_keys=%d err=%v", foreign, err)
	}
	if err = s.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
		t.Fatalf("busy_timeout=%d err=%v", busy, err)
	}
	if err = s.db.QueryRow("PRAGMA synchronous").Scan(&syncMode); err != nil || syncMode != 2 {
		t.Fatalf("synchronous=%d err=%v", syncMode, err)
	}
	if err = s.close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Versions 2-3 are the coordination domain and are legitimate; the first
	// genuinely future version this binary must refuse is 4.
	if _, err = db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err = openStore(root); err == nil {
		t.Fatal("future schema version was accepted")
	}
}
