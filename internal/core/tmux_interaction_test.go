package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTMUXLiteralInteractionUsesSeparateCRSubmit(t *testing.T) {
	realTMUX, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	socket := "cp-interaction-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	if socket == "cockpit" {
		t.Fatal("test selected live socket")
	}
	output := filepath.Join(root, "input")
	if out, err := exec.Command(realTMUX, "-L", socket, "new-session", "-d", "-s", "interaction", "bash", "-c", "IFS= read -r line; printf %s \"$line\" >"+output+"; sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("new tmux session: %v: %s", err, out)
	}
	defer func() { _ = exec.Command(realTMUX, "-L", socket, "kill-server").Run() }()
	paneOut, err := exec.Command(realTMUX, "-L", socket, "display-message", "-p", "-t", "interaction:0.0", "#{pane_id}").Output()
	if err != nil {
		t.Fatal(err)
	}
	pane := strings.TrimSpace(string(paneOut))
	shimDir := filepath.Join(root, "bin")
	if err = os.Mkdir(shimDir, 0700); err != nil {
		t.Fatal(err)
	}
	invocations := filepath.Join(root, "tmux-invocations")
	shim := filepath.Join(shimDir, "tmux")
	if err = os.WriteFile(shim, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$COCKPIT_TMUX_INVOCATIONS\"\nexec \"$COCKPIT_REAL_TMUX\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCKPIT_TMUX_INVOCATIONS", invocations)
	t.Setenv("COCKPIT_REAL_TMUX", realTMUX)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	text := "Codex literal submit behavior"
	driver := tmux{socket: socket, trace: filepath.Join(root, "driver.trace")}
	started := time.Now()
	if err = driver.interact(pane, "nudge", text); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < literalSubmitSettle {
		t.Fatalf("literal submit settled for only %s, want at least %s", elapsed, literalSubmitSettle)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, readErr := os.ReadFile(output); readErr == nil {
			if string(got) != text {
				t.Fatalf("literal input=%q", got)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = os.Stat(output); err != nil {
		t.Fatal("literal text was not submitted with C-m")
	}
	trace, err := os.ReadFile(filepath.Join(root, "driver.trace"))
	if err != nil || strings.Count(string(trace), "interaction.nudge text=[REDACTED]") != 1 || strings.Contains(string(trace), text) {
		t.Fatalf("private interaction trace=%q err=%v", trace, err)
	}
	called, err := os.ReadFile(invocations)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(called)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "send-keys -t "+pane+" -l "+text) || !strings.Contains(lines[1], "send-keys -t "+pane+" C-m") || strings.Contains(string(called), "Enter") {
		t.Fatalf("literal and submit were not separate C-m calls: %q", called)
	}
}
