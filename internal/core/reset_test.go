package core

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// seedProjection plants a durable pane/workspace projection of the shape a live grid leaves behind, so the
// reset is exercised against realistic state rather than an empty database.
func seedProjection(t *testing.T, root string, panes int) *store {
	t.Helper()
	st, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec("INSERT INTO meta(k,v) VALUES('fingerprint','cpf_seed')"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < panes; i++ {
		wref, pref := "cpw_seed", "cpp_seed"
		if _, err = st.db.Exec("INSERT OR IGNORE INTO workspaces(ref,window_id,name,generation,version) VALUES(?,?,?,1,1)",
			wref+itoa(i), "@"+itoa(i), "w"+itoa(i)); err != nil {
			t.Fatal(err)
		}
		if _, err = st.db.Exec("INSERT INTO panes(ref,workspace_ref,window_id,pane_id,generation,version,badge) VALUES(?,?,?,?,1,1,'')",
			pref+itoa(i), wref+itoa(i), "@"+itoa(i), "%"+itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func itoa(i int) string { return strconv.Itoa(i) }

// holdLease takes the controller lease the way a running (or crash-looping) daemon does.
func holdLease(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		t.Fatal(err)
	}
	return f
}

func TestResetPanesDryRunMutatesNothing(t *testing.T) {
	root := t.TempDir()
	st := seedProjection(t, root, 3)
	st.db.Close()

	report, err := ResetPanes(root, nil, "gareth")
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if !report.DryRun || report.Panes != 3 || report.Workspaces != 3 {
		t.Fatalf("unexpected dry-run report: %+v", report)
	}
	if !strings.Contains(report.String(), "would retire") {
		t.Fatalf("dry-run report does not read as a projection: %q", report.String())
	}
	st2, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.db.Close()
	var panes int
	if err = st2.db.QueryRow("SELECT count(*) FROM panes").Scan(&panes); err != nil {
		t.Fatal(err)
	}
	if panes != 3 {
		t.Fatalf("dry run mutated the projection: %d panes remain", panes)
	}
}

func TestResetPanesRequiresMatchingConfirmation(t *testing.T) {
	root := t.TempDir()
	st := seedProjection(t, root, 3)
	st.db.Close()

	wrong := 2
	if _, err := ResetPanes(root, &wrong, "gareth"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a stale confirmation was accepted: %v", err)
	}
	st2, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.db.Close()
	var panes int
	if err = st2.db.QueryRow("SELECT count(*) FROM panes").Scan(&panes); err != nil {
		t.Fatal(err)
	}
	if panes != 3 {
		t.Fatalf("a refused confirmation still mutated state: %d panes remain", panes)
	}
}

func TestResetPanesRequiresAnOperator(t *testing.T) {
	root := t.TempDir()
	st := seedProjection(t, root, 1)
	st.db.Close()
	if _, err := ResetPanes(root, nil, ""); err == nil || !strings.Contains(err.Error(), "operator") {
		t.Fatalf("anonymous retirement was permitted: %v", err)
	}
}

func TestResetPanesRefusesPreparedOperations(t *testing.T) {
	root := t.TempDir()
	st := seedProjection(t, root, 2)
	if _, err := st.db.Exec("INSERT INTO operations(ref,caller,method,idem_key,intent,status,pane_ref,badge,target_version,result,created_at) VALUES('cpo_p','c','interaction.nudge','ik','i','prepared','cpp_seed0','',2,'null',0)"); err != nil {
		t.Fatal(err)
	}
	st.db.Close()

	confirm := 2
	if _, err := ResetPanes(root, &confirm, "gareth"); err == nil || !strings.Contains(err.Error(), "prepared") {
		t.Fatalf("retirement proceeded over an unresolved prepared operation: %v", err)
	}
}

// The whole point of the command: after retirement the successor check has nothing to look for, which is what
// unblocks startup. Terminal operations and the audit trail must survive it.
func TestResetPanesClearsBothProjectionsAndAudits(t *testing.T) {
	root := t.TempDir()
	st := seedProjection(t, root, 4)
	if _, err := st.db.Exec("INSERT INTO operations(ref,caller,method,idem_key,intent,status,pane_ref,badge,target_version,result,created_at) VALUES('cpo_done','c','interaction.nudge','ik','i','effect-delivered-unconfirmed','cpp_seed0','',2,'null',0)"); err != nil {
		t.Fatal(err)
	}
	st.db.Close()

	confirm := 4
	report, err := ResetPanes(root, &confirm, "gareth")
	if err != nil {
		t.Fatalf("authorised retirement failed: %v", err)
	}
	if report.DryRun || report.Panes != 4 {
		t.Fatalf("unexpected report: %+v", report)
	}

	st2, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.db.Close()
	for _, probe := range []struct {
		q    string
		want int
		what string
	}{
		{"SELECT count(*) FROM panes", 0, "panes"},
		{"SELECT count(*) FROM workspaces", 0, "workspaces"},
		{"SELECT count(*) FROM operations", 1, "operations (must survive: they carry replay protection)"},
		{"SELECT count(*) FROM audit WHERE method='reset-panes' AND caller='gareth'", 1, "authored audit row"},
	} {
		var got int
		if err = st2.db.QueryRow(probe.q).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != probe.want {
			t.Fatalf("%s: got %d, want %d", probe.what, got, probe.want)
		}
	}
	// The durable fingerprint is deliberately left alone — reconcile mints the next one itself.
	var fp string
	if err = st2.db.QueryRow("SELECT v FROM meta WHERE k='fingerprint'").Scan(&fp); err != nil || fp != "cpf_seed" {
		t.Fatalf("fingerprint was disturbed: %q %v", fp, err)
	}
}

func TestResetPanesRefusesWhileTheLeaseIsHeld(t *testing.T) {
	root := t.TempDir()
	st := seedProjection(t, root, 1)
	st.db.Close()

	held := holdLease(t, filepath.Join(root, "controller.lock"))
	defer held.Close()

	if _, err := ResetPanes(root, nil, "gareth"); err == nil || !strings.Contains(err.Error(), "controller lease unavailable") {
		t.Fatalf("reset raced a live controller: %v", err)
	}
}
