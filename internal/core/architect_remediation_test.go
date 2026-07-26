package core_test

import (
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gareth/cockpit-core/internal/core"
	_ "modernc.org/sqlite"
)

func TestFreshRootCannotAdoptStampedServer(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	root := t.TempDir()
	sock := filepath.Join(root, "control.sock")
	bad := exec.Command(f.bin, "daemon", "--test-root", root, "--socket", sock, "--tmux-socket", f.tmux, "--credentials-file", f.credentials)
	out, err := bad.CombinedOutput()
	if err == nil || (!strings.Contains(string(out), "already controller-stamped") && !strings.Contains(string(out), "server lease unavailable")) {
		t.Fatalf("fresh root adopted stamped server: %v %s", err, out)
	}
	if _, err = os.Stat(filepath.Join(root, "control.db")); !os.IsNotExist(err) {
		t.Fatal("fresh rejected root opened a store")
	}
	if _, err = os.Stat(filepath.Join(root, "driver.trace")); !os.IsNotExist(err) {
		t.Fatal("fresh rejected root wrote a driver trace")
	}
}

func TestCloseOnlyUnlinksOwnedSocketInode(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "control.sock")
	tmuxSocket := "cp-it-close-" + time.Now().Format("150405.000000000")
	run(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "slice", "sleep 60")
	defer exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run()
	d, err := core.NewDaemon(root, socket, tmuxSocket)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve()
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if _, e := os.Lstat(socket); e == nil {
			break
		}
	}
	_ = os.Remove(socket)
	foreign, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("Close unlinked foreign replacement: %v", err)
	}
	_ = foreign.Close()
}

func TestPreparedPartialEffectFencesPane(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	f.stop()
	p := f.pane
	ref := p["paneRef"].(string)
	pid := p["locator"].(map[string]any)["paneId"].(string)
	version := int64(p["resourceVersion"].(float64))
	db, err := sql.Open("sqlite", filepath.Join(f.root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO operations(ref,caller,method,idem_key,intent,status,pane_ref,badge,target_version,result,created_at) VALUES(?,?,?,?,?,'prepared',?,?,?,'null',?)", "cpo_partial0000000000000000000000000", "test-local-operator", "metadata.set_display", ik(0, 987), "partial", ref, "partial", version+1, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", pid, "@cockpit_badge", "partial")
	f.start(false)
	if r := f.call("pane.inspect", map[string]any{"paneRef": ref}); r["result"] == nil {
		t.Fatal("fenced pane not inspectable")
	}
	if r := f.call("metadata.set_display", f.badgeParams("must-fence", ik(0, 988), float64(version))); !strings.Contains(stringify(r), "CONTROLLER_NOT_READY") {
		t.Fatalf("partial prepared pane remained writable: %#v", r)
	}
}

func TestConflictingIdentityStampsFenceBeforeAdoption(t *testing.T) {
	for _, tc := range []struct{ name, option, value string }{
		{"duplicate-pane", "@cockpit_pane_ref", "duplicate"},
		{"partial-pane", "@cockpit_pane_generation", ""},
		{"duplicate-workspace", "@cockpit_workspace_ref", "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			defer f.close()
			f.stop()
			if tc.name == "duplicate-pane" {
				v := f.pane["paneRef"].(string)
				run(t, "tmux", "-L", f.tmux, "set-option", "-p", "-t", f.nonTargetPane, tc.option, v)
			} else if tc.name == "partial-pane" {
				run(t, "tmux", "-L", f.tmux, "set-option", "-pu", "-t", f.targetPane, tc.option)
			} else {
				run(t, "tmux", "-L", f.tmux, "new-window", "-d", "-t", "slice", "-n", "other", "sleep 60")
				w := strings.TrimSpace(string(mustOutput(t, "tmux", "-L", f.tmux, "display-message", "-p", "-t", "slice:0", "#{@cockpit_workspace_ref}")))
				run(t, "tmux", "-L", f.tmux, "set-option", "-w", "-t", "slice:1", tc.option, w)
			}
			bad := exec.Command(f.bin, "daemon", "--test-root", f.root, "--socket", f.socket, "--tmux-socket", f.tmux, "--credentials-file", f.credentials)
			if out, err := bad.CombinedOutput(); err == nil || !strings.Contains(string(out), "CONTROLLER_NOT_READY") {
				t.Fatalf("conflicting stamp adopted: %v %s", err, out)
			}
		})
	}
}
func stringify(v any) string { return strings.ReplaceAll(strings.TrimSpace(toJSON(v)), " ", "") }
func toJSON(v any) string    { b, _ := json.Marshal(v); return string(b) }
