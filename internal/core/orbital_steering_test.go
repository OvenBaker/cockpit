package core

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// BRF-039 — the `orbital` profile grants exactly the three capabilities Orbital's brief-studio reply path
// needs. Before this, validClaimedProfile accepted the NAME while profileCapabilities returned an empty set,
// so a grant claiming it failed credential loading and rejected the whole file — taking controller auth down
// for every client, not just Orbital.
func TestOrbitalProfileGrantsExactlyThreeCapabilities(t *testing.T) {
	want := []string{"state:read", "capture:sanitized", "interaction:nudge"}
	got := profileCapabilities("orbital")
	if len(got) != len(want) {
		t.Fatalf("orbital profile capability set is %v, want exactly %v", got, want)
	}
	for _, capability := range want {
		if !has(got, capability) {
			t.Fatalf("orbital profile is missing %s: %v", capability, got)
		}
	}
	// Everything the narrowest EXISTING profile would have brought with it stays unreachable by profile.
	for _, forbidden := range []string{"metadata:write", "operations:read", "events:wait", "coord:read",
		"coord:write", "coord:admin", "interaction:pause", "interaction:compact", "interaction:resume"} {
		if has(got, forbidden) {
			t.Fatalf("orbital profile grants %s, which it must not: %v", forbidden, got)
		}
	}
	if !validClaimedProfile("orbital") {
		t.Fatal("the controller no longer accepts the orbital profile name")
	}

	root := t.TempDir()
	registry := filepath.Join(root, "clients.json")
	write := func(capabilities []string) error {
		b, err := json.Marshal(map[string]any{"version": 1, "clients": []any{map[string]any{
			"credential": "orbital-brief-studio-credential", "clientId": "orbital-brief-studio",
			"profile": "orbital", "capabilities": capabilities,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return os.WriteFile(registry, b, 0600)
	}
	if err := write(want); err != nil {
		t.Fatal(err)
	}
	auth, err := loadAuthenticator(registry)
	if err != nil {
		t.Fatalf("a credential grant claiming orbital still fails to load: %v", err)
	}
	grant, ok := auth.verify("orbital-brief-studio-credential", "orbital-brief-studio", "orbital")
	if !ok || !has(grant.Capabilities, "interaction:nudge") || has(grant.Capabilities, "metadata:write") {
		t.Fatalf("orbital grant is not bound to its profile capabilities: %#v", grant)
	}
	if err = write(append(append([]string{}, want...), "metadata:write")); err != nil {
		t.Fatal(err)
	}
	if _, err = loadAuthenticator(registry); err == nil {
		t.Fatal("a grant claiming a capability outside the orbital profile was admitted")
	}
}

// strandNudge fakes exactly what an interrupted `interaction.nudge` leaves behind: a durable `prepared`
// operations row recorded before the tmux effect, plus the tmux-side version bump that landed before the
// process died. `fence` additionally drives the pane to the fenced state the effect-error path produces —
// the residue nothing in the controller used to clear.
func strandNudge(t *testing.T, d *daemon, tmuxSocket string, p pane, fence bool) {
	t.Helper()
	intent := fmt.Sprintf("nudge:%s:%d:%d:%s", p.Ref, p.Generation, p.Version, digest("brief-studio notice"))
	status := "prepared"
	if fence {
		status = "recovery-required"
	}
	if _, err := d.st.db.Exec(
		"INSERT INTO operations(ref,caller,method,idem_key,intent,status,pane_ref,badge,target_version,result,created_at) VALUES(?,?,?,?,?,?,?,'',?,'null',?)",
		"cpo_strand", "orbital-brief-studio", "interaction.nudge", "ik_1_"+strings.Repeat("a", 32), intent, status,
		p.Ref, p.Version, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if fence {
		if _, err := d.st.db.Exec("UPDATE panes SET fenced=1 WHERE ref=?", p.Ref); err != nil {
			t.Fatal(err)
		}
	}
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", p.PaneID,
		"@cockpit_pane_version", fmt.Sprint(p.Version+1))
}

func orbitalTestAuth(t *testing.T, root, credential string) *authenticator {
	t.Helper()
	registry := filepath.Join(root, "clients.json")
	b, err := json.Marshal(map[string]any{"version": 1, "clients": []any{map[string]any{
		"credential": credential, "clientId": "orbital-brief-studio", "profile": "orbital",
		"capabilities": []string{"state:read", "capture:sanitized", "interaction:nudge"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registry, b, 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := loadAuthenticator(registry)
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

// BRF-025 (a) + INV-12 — an interrupted nudge is recovered on an ORDINARY same-fingerprint restart, with no
// operator step: the prepared row resolves, the one-version divergence reconciles, and a pane the strand
// fenced is unfenced so it can accept a typed interaction again.
func TestStrandedNudgeRecoversOnPlainRestart(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		name := "unfenced-strand"
		if fenced {
			name = "fenced-strand"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			auth := orbitalTestAuth(t, root, "strand-plain-"+name)
			tmuxSocket := fmt.Sprintf("cp-strand-plain-%s-%d", name, time.Now().UnixNano())
			defer func() { _ = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run() }()
			runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "stranded", "sleep 600")

			d, err := newDaemon(root, filepath.Join(root, "control.sock"), tmuxSocket, auth, true)
			if err != nil {
				t.Fatal(err)
			}
			panes, err := d.st.panes()
			if err != nil || len(panes) != 1 {
				d.Close()
				t.Fatalf("initial inventory: %#v %v", panes, err)
			}
			original := panes[0]
			strandNudge(t, d, tmuxSocket, original, fenced)
			d.Close()

			restarted, err := newDaemon(root, filepath.Join(root, "control.sock"), tmuxSocket, auth, true)
			if err != nil {
				t.Fatalf("startup refused over a nudge strand: %v", err)
			}
			defer restarted.Close()
			got, err := restarted.st.pane(original.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != original.Version+1 {
				t.Fatalf("the one-version divergence was not reconciled: durable %d, expected %d", got.Version, original.Version+1)
			}
			if got.Fenced {
				t.Fatal("the pane is still fenced after recovery — the reply path can permanently disable its own pane")
			}
			var status string
			if err = restarted.st.db.QueryRow("SELECT status FROM operations WHERE ref=?", "cpo_strand").Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "effect-delivered-unconfirmed" {
				t.Fatalf("stranded nudge resolved to %q; delivery is not completion but it must be resolved", status)
			}
		})
	}
}

// BRF-025 (b) — the compound failure: a nudge strand that coincides with a Cockpit replacement. Successor
// validation used to refuse on BOTH residues (a prepared row at all, and any version divergence), so the
// fence was permanent. Recovery now runs first and the fence is narrowed to admit exactly this strand.
func TestStrandedNudgeRecoversAcrossFingerprintSuccessor(t *testing.T) {
	root := t.TempDir()
	auth := orbitalTestAuth(t, root, "strand-successor")
	tmuxSocket := fmt.Sprintf("cp-strand-successor-%d", time.Now().UnixNano())
	defer func() { _ = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run() }()
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "stranded", "sleep 600")

	d, err := newDaemon(root, filepath.Join(root, "control.sock"), tmuxSocket, auth, true)
	if err != nil {
		t.Fatal(err)
	}
	panes, err := d.st.panes()
	if err != nil || len(panes) != 1 {
		d.Close()
		t.Fatalf("initial inventory: %#v %v", panes, err)
	}
	original := panes[0]
	strandNudge(t, d, tmuxSocket, original, false)
	d.Close()

	// The Cockpit server is replaced and the grid restored with the stamps the strand left behind.
	if err = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run(); err != nil {
		t.Fatal(err)
	}
	restoreStrandedGrid(t, tmuxSocket, original, original.Version+1)

	rebound, err := newDaemon(root, filepath.Join(root, "control.sock"), tmuxSocket, auth, true)
	if err != nil {
		t.Fatalf("successor startup refused over a recoverable nudge strand: %v", err)
	}
	defer rebound.Close()
	got, err := rebound.st.pane(original.Ref)
	if err != nil || got.Version != original.Version+1 || got.Fenced {
		t.Fatalf("strand was not reconciled across the successor: %#v %v", got, err)
	}
}

// BRF-014 — the fence stays exactly as tight everywhere else: a divergence GREATER than one version, and a
// one-version divergence unaccompanied by its own prepared interaction operation, both still refuse.
func TestSuccessorRefusesDivergenceOutsideTheRecoverableStrand(t *testing.T) {
	cases := []struct {
		name    string
		bump    int64
		prepare bool
	}{
		{name: "two-version-divergence-with-strand", bump: 2, prepare: true},
		{name: "one-version-divergence-without-strand", bump: 1, prepare: false},
	}
	for _, row := range cases {
		t.Run(row.name, func(t *testing.T) {
			root := t.TempDir()
			auth := orbitalTestAuth(t, root, "fence-"+row.name)
			tmuxSocket := fmt.Sprintf("cp-fence-%s-%d", row.name, time.Now().UnixNano())
			defer func() { _ = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run() }()
			runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "fenced", "sleep 600")

			d, err := newDaemon(root, filepath.Join(root, "control.sock"), tmuxSocket, auth, true)
			if err != nil {
				t.Fatal(err)
			}
			panes, err := d.st.panes()
			if err != nil || len(panes) != 1 {
				d.Close()
				t.Fatalf("initial inventory: %#v %v", panes, err)
			}
			original := panes[0]
			if row.prepare {
				strandNudge(t, d, tmuxSocket, original, false)
			}
			d.Close()
			if err = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run(); err != nil {
				t.Fatal(err)
			}
			restoreStrandedGrid(t, tmuxSocket, original, original.Version+row.bump)

			if _, err = newDaemon(root, filepath.Join(root, "control.sock"), tmuxSocket, auth, true); err == nil ||
				!strings.Contains(err.Error(), "CONTROLLER_NOT_READY") {
				t.Fatalf("a divergence outside the recoverable strand was admitted: %v", err)
			}
		})
	}
}

func restoreStrandedGrid(t *testing.T, tmuxSocket string, original pane, version int64) {
	t.Helper()
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "stranded", "sleep 600")
	newPane := strings.TrimSpace(liveEquivalentOutput(t, "tmux", "-L", tmuxSocket, "list-panes", "-t", "stranded", "-F", "#{pane_id}"))
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-w", "-t", "stranded:0", "@cockpit_workspace_ref", original.WorkspaceRef)
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", newPane, "@cockpit_pane_ref", original.Ref)
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", newPane, "@cockpit_pane_generation", fmt.Sprint(original.Generation))
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", newPane, "@cockpit_pane_version", fmt.Sprint(version))
}
