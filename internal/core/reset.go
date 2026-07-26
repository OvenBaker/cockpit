package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Offline recovery for the one permanent fence the controller can reach on its own.
//
// After the tmux server is replaced (a reboot, a `kill-server`), Cockpit restores the grid but the
// controller's per-pane stamps do not come back with it. validateFingerprintSuccessor then finds every
// durable pane row absent from the successor grid and refuses startup — correctly, since it cannot tell a
// deliberate replacement from a partial or hostile one. Nothing clears that state, so the refusal is
// permanent and the daemon crash-loops.
//
// The escape is deliberately NOT automatic (owner decision): retirement is authored by the operator, states
// the row count it will retire, and lands in the audit trail. Automatic retirement would silently orphan
// every recorded pane binding with no human on the record.
//
// Retiring panes alone is not sufficient. Both panes.pane_id and workspaces.window_id are UNIQUE, while
// reconcile's re-inventory inserts carry only ON CONFLICT(ref) clauses — so a stale row in either table
// collides with the freshly minted ref for the same tmux id and fails the rebind on a UNIQUE violation.
// Both tables are therefore cleared together.
//
// Deliberately preserved:
//   - audit — the append-only trail, including this retirement.
//   - operations — terminal rows keep UNIQUE(caller,method,idem_key) doing its job, so a replayed
//     idempotency key still conflicts instead of being re-attempted. They reference panes without a foreign
//     key and never block the rebind; deleting them would weaken replay protection for no gain.
//   - meta.fingerprint — with panes empty the successor check has nothing to look for and passes, after
//     which reconcile mints the next fingerprint itself. One less mutation on a recovery path.

// ResetReport is what the reset did, or would do in a dry run.
type ResetReport struct {
	Panes      int
	Workspaces int
	Operations int
	DryRun     bool
	Operator   string
}

func (r ResetReport) String() string {
	mode := "retired"
	if r.DryRun {
		mode = "would retire"
	}
	return fmt.Sprintf("%s %d pane rows and %d workspace rows (operations kept: %d, audit preserved)",
		mode, r.Panes, r.Workspaces, r.Operations)
}

// ResetPanes retires the durable pane and workspace projections so a restored Cockpit grid can be
// re-inventoried. With confirm nil it mutates nothing and only reports. With confirm non-nil the value must
// equal the pane-row count exactly — a stale count means the operator is authorising something other than
// what they read, and is refused.
//
// It takes the controller lease for the duration, so it can never race a live or restarting daemon.
func ResetPanes(root string, confirm *int, operator string) (ResetReport, error) {
	var report ResetReport
	if !filepath.IsAbs(root) {
		return report, errors.New("runtime root must be absolute")
	}
	if operator == "" {
		return report, errors.New("an operator identity is required: retirement is authored, never anonymous")
	}
	clean := filepath.Clean(root)
	report.Operator = operator
	report.DryRun = confirm == nil

	// The lease is the whole concurrency story: a running daemon holds it, and a crash-looping one holds it
	// intermittently. Failing closed here is correct — the operator stops the unit and retries.
	lf, err := os.OpenFile(filepath.Join(clean, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return report, err
	}
	defer lf.Close()
	if err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return report, fmt.Errorf("controller lease unavailable (stop the controller unit first): %w", err)
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()

	st, err := openStore(clean)
	if err != nil {
		return report, err
	}
	defer st.db.Close()

	// Same precedence the successor check uses: a prepared operation is an unresolved effect, and clearing
	// panes underneath one would strand it. Refuse rather than reorder the operator's problem.
	var prepared int
	if err = st.db.QueryRow("SELECT count(*) FROM operations WHERE status='prepared'").Scan(&prepared); err != nil {
		return report, err
	}
	if prepared != 0 {
		return report, fmt.Errorf("%d prepared operation(s) must be resolved before pane rows can be retired", prepared)
	}
	if err = st.db.QueryRow("SELECT count(*) FROM panes").Scan(&report.Panes); err != nil {
		return report, err
	}
	if err = st.db.QueryRow("SELECT count(*) FROM workspaces").Scan(&report.Workspaces); err != nil {
		return report, err
	}
	if err = st.db.QueryRow("SELECT count(*) FROM operations").Scan(&report.Operations); err != nil {
		return report, err
	}
	if report.DryRun {
		return report, nil
	}
	if *confirm != report.Panes {
		return report, fmt.Errorf("confirmation %d does not match the %d pane rows present; re-read and confirm the current count",
			*confirm, report.Panes)
	}

	tx, err := st.db.Begin()
	if err != nil {
		return report, err
	}
	// panes first: it references workspaces, and foreign_keys is ON.
	if err = mustTx(tx, "DELETE FROM panes"); err == nil {
		err = mustTx(tx, "DELETE FROM workspaces")
	}
	if err == nil {
		err = mustTx(tx, "INSERT INTO audit(at,caller,method,pane_ref,before_digest,after_digest) VALUES(?,?,?,?,?,?)",
			time.Now().Unix(), operator, "reset-panes", "",
			fmt.Sprintf("panes:%d workspaces:%d", report.Panes, report.Workspaces), "retired")
	}
	if err != nil {
		_ = tx.Rollback()
		return report, err
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}
