package core_test

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestServerLeaseTwoFreshRootsProcess is deliberately an OS-process oracle:
// NewDaemon alone cannot establish authenticated readiness or loser isolation.
func TestServerLeaseTwoFreshRootsProcess(t *testing.T) {
	tmuxSocket := fmt.Sprintf("cp-it-lease-process-%d", time.Now().UnixNano())
	run(t, "tmux", "-L", tmuxSocket, "new-session", "-d", "-s", "slice", "sleep 600")
	defer exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run()
	bin := buildTestBinary(t)
	roots := []string{t.TempDir(), t.TempDir()}
	cmds := make([]*exec.Cmd, 2)
	var wg sync.WaitGroup
	for i := range roots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmds[i] = daemonProcess(bin, roots[i], tmuxSocket)
			if err := cmds[i].Start(); err != nil {
				t.Errorf("start contender %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	ready := -1
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ready < 0 {
		for i := range roots {
			if cmds[i] != nil && healthySocket(filepath.Join(roots[i], "control.sock")) {
				ready = i
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ready < 0 {
		for _, c := range cmds {
			if c != nil {
				_ = c.Process.Kill()
				_ = c.Wait()
			}
		}
		t.Fatal("neither contender became authenticated and ready")
	}
	loser := 1 - ready
	loserExit := make(chan error, 1)
	go func() { loserExit <- cmds[loser].Wait() }()
	var loserErr error
	select {
	case loserErr = <-loserExit:
	case <-time.After(3 * time.Second):
		_ = cmds[loser].Process.Kill()
		<-loserExit
		_ = cmds[ready].Process.Kill()
		_ = cmds[ready].Wait()
		t.Fatal("lease loser did not exit within bounded deadline")
	}
	if loserErr == nil {
		_ = cmds[ready].Process.Kill()
		_ = cmds[ready].Wait()
		t.Fatal("second fresh-root daemon unexpectedly stayed alive")
	}
	if _, err := os.Stat(filepath.Join(roots[loser], "control.db")); !os.IsNotExist(err) {
		t.Fatalf("lease loser created control.db: %v", err)
	}
	if _, err := os.Stat(filepath.Join(roots[loser], "driver.trace")); !os.IsNotExist(err) {
		t.Fatalf("lease loser mutated driver trace: %v", err)
	}
	_ = cmds[ready].Process.Kill()
	_ = cmds[ready].Wait()

	third := t.TempDir()
	bad := daemonProcess(bin, third, tmuxSocket)
	out, err := bad.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("already controller-stamped")) {
		t.Fatalf("third root was not rejected by persisted fingerprint: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(third, "control.db")); !os.IsNotExist(err) {
		t.Fatalf("fingerprint-rejected root created control.db: %v", err)
	}

	empty := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(empty, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=0"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	bad = daemonProcess(bin, empty, tmuxSocket)
	out, err = bad.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("already controller-stamped")) {
		t.Fatalf("precreated empty DB did not fail closed on fingerprint: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(empty, "driver.trace")); !os.IsNotExist(err) {
		t.Fatalf("precreated DB rejection mutated tmux: %v", err)
	}
}

func buildTestBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cockpit-core")
	cmd := exec.Command("go", "build", "-tags", "cockpit_test", "-buildvcs=false", "-o", bin, "./cmd/cockpit-core")
	cmd.Dir = filepath.Clean(filepath.Join("..", ".."))
	cmd.Env = append(os.Environ(), "GOCACHE=/tmp/cp-core-go-cache")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	return bin
}
func daemonProcess(bin, root, tmuxSocket string) *exec.Cmd {
	c := exec.Command(bin, "daemon", "--test-root", root, "--socket", filepath.Join(root, "control.sock"), "--tmux-socket", tmuxSocket)
	c.Env = append(os.Environ(), "COCKPIT_TEST_BARRIER_DIR="+filepath.Join(root, "barriers"))
	return c
}
func healthySocket(socket string) bool {
	c, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	writeRawFrame := func(v any) error {
		b, _ := json.Marshal(v)
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], uint32(len(b)))
		_, err = c.Write(append(h[:], b...))
		return err
	}
	if writeRawFrame(map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": "lease-health", "claimedProfile": "read-only", "credential": "test-read"}}) != nil {
		return false
	}
	if _, err = readRawErr(c); err != nil {
		return false
	}
	if writeRawFrame(map[string]any{"jsonrpc": "2.0", "id": "health", "method": "controller.health", "params": map[string]any{}}) != nil {
		return false
	}
	r, err := readRawErr(c)
	if err != nil || r["result"] == nil {
		return false
	}
	ready, _ := r["result"].(map[string]any)["ready"].(bool)
	return ready
}
