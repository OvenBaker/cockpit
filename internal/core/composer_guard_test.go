package core

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The mid-prompt clobber: a pane can be controller-`waiting` (its agent ended
// the turn) while the OPERATOR has a half-typed draft in the composer — no hook
// fires on human keystrokes, so no state machine can see it. A nudge would
// append its notice to the draft and C-m would submit the lot. The guard must
// refuse strictly pre-effect (same idempotency key retries cleanly), and must
// deliver once the composer is clear.
func TestNudgeRefusesOverAnOccupiedComposerAndRetriesClean(t *testing.T) {
	root := t.TempDir()
	auth := orbitalTestAuth(t, root, "composer-guard")
	tmuxSocket := fmt.Sprintf("cp-composer-%d", time.Now().UnixNano())
	defer func() { _ = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run() }()
	// A fake claude screen: transcript, then the composer glyph with a draft.
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "composer",
		"bash -c 'printf \"transcript line\\n\\u276f half-typed operator draft\\n\"; sleep 600'")
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", "composer:0.0", "@agent", "claude")
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", "composer:0.0", "@state", "idle")

	d, err := newDaemon(root, filepath.Join(root, "control.sock"), tmuxSocket, auth, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	panes, err := d.st.panes()
	if err != nil || len(panes) != 1 {
		t.Fatalf("inventory: %#v %v", panes, err)
	}
	p := panes[0]
	key := fmt.Sprintf("ik_%d_%032d", time.Now().Unix(), 1)
	params := func() interactionParams {
		x := interactionParams{Protocol: Protocol, Deadline: time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
			IdempotencyKey: key, PaneRef: p.Ref, Text: "Orbital: notice",
			Expectations: []paneExpectation{{Kind: "pane", PaneRef: p.Ref, Generation: p.Generation, ResourceVersion: p.Version}}}
		x.Expectations[0].Material.Lifecycle = "active"
		x.Expectations[0].Material.ObservedState = "waiting"
		return x
	}
	if _, err = d.interact("orbital-brief-studio", "interaction.nudge", params()); err == nil ||
		!strings.Contains(err.Error(), "PANE_COMPOSING") {
		t.Fatalf("a drafted composer was not refused: %v", err)
	}
	// Pre-effect refusal leaves no residue: no operation row, no version bump, no fence.
	var count int
	if err = d.st.db.QueryRow("SELECT COUNT(*) FROM operations WHERE method='interaction.nudge'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("a refused nudge left an operation row: %d %v", count, err)
	}
	if got, e := d.st.pane(p.Ref); e != nil || got.Version != p.Version || got.Fenced {
		t.Fatalf("a refused nudge disturbed the pane: %#v %v", got, e)
	}

	// The composer clears; the SAME idempotency key must now deliver.
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "respawn-pane", "-k", "-t", p.PaneID,
		"bash -c 'printf \"transcript line\\n\\u276f \\n\"; IFS= read -r line; sleep 600'")
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", p.PaneID, "@agent", "claude")
	runLiveEquivalent(t, "tmux", "-L", tmuxSocket, "set-option", "-p", "-t", p.PaneID, "@state", "idle")
	time.Sleep(200 * time.Millisecond)
	result, err := d.interact("orbital-brief-studio", "interaction.nudge", params())
	if err != nil {
		t.Fatalf("a clear composer still refused: %v", err)
	}
	if op, ok := result.(map[string]any); !ok || op["status"] != "effect-delivered-unconfirmed" {
		t.Fatalf("delivery result: %#v", result)
	}
}
