package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Codex materializes a tmux literal paste asynchronously. Keep this bounded
// settle private to the typed driver so the fixed submit key cannot overtake
// the literal text. It is not a caller-configurable delay or a public key API.
const literalSubmitSettle = 150 * time.Millisecond

type tmux struct {
	socket, trace    string
	allowLiveCockpit bool
}

func (t tmux) run(args ...string) ([]byte, error) {
	return t.runTrace("", "", args...)
}
func (t tmux) runSensitive(traceRecord, errorLabel string, args ...string) ([]byte, error) {
	return t.runTrace(traceRecord, errorLabel, args...)
}
func (t tmux) runUntraced(errorLabel string, args ...string) ([]byte, error) {
	argv := append([]string{"-L", t.socket}, args...)
	if t.socket == "cockpit" && !t.allowLiveCockpit {
		return nil, derr("INTERNAL", "live cockpit socket refused")
	}
	c := exec.Command("tmux", argv...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, e := c.Output()
	if e != nil {
		return nil, fmt.Errorf("tmux %s: %w: %s", errorLabel, e, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
func (t tmux) runTrace(traceRecord, errorLabel string, args ...string) ([]byte, error) {
	argv := append([]string{"-L", t.socket}, args...)
	if t.socket == "cockpit" && !t.allowLiveCockpit {
		return nil, derr("INTERNAL", "live cockpit socket refused")
	}
	if t.trace != "" {
		f, e := os.OpenFile(t.trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e == nil {
			if traceRecord == "" {
				traceRecord = "tmux " + strings.Join(argv, " ")
			}
			_, _ = fmt.Fprintln(f, traceRecord)
			_ = f.Close()
		}
	}
	c := exec.Command("tmux", argv...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, e := c.Output()
	if e != nil {
		if errorLabel != "" {
			return nil, fmt.Errorf("tmux %s: %w: %s", errorLabel, e, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("tmux %q: %w: %s", args, e, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
func (t tmux) setGlobal(key, value string) error {
	_, e := t.run("set-option", "-g", key, value)
	return e
}
func (t tmux) setWindow(window, key, value string) error {
	_, e := t.run("set-option", "-w", "-t", window, key, value)
	return e
}
func (t tmux) setPane(pane, key, value string) error {
	_, e := t.run("set-option", "-p", "-t", pane, key, value)
	return e
}

// setPaneBadgeVersion is the indivisible Slice-1 driver plan: one tmux client
// invocation performs both controlled pane option writes in order.
func (t tmux) setPaneBadgeVersion(pane, badge string, version int64) error {
	_, e := t.run("set-option", "-p", "-t", pane, "@cockpit_badge", badge, ";", "set-option", "-p", "-t", pane, "@cockpit_pane_version", fmt.Sprint(version))
	return e
}
func (t tmux) setPaneVersion(pane string, version int64) error {
	_, e := t.run("set-option", "-p", "-t", pane, "@cockpit_pane_version", fmt.Sprint(version))
	return e
}
func (t tmux) paneOption(pane, key string) (string, error) {
	b, e := t.run("display-message", "-p", "-t", pane, "#{"+key+"}")
	return strings.TrimSpace(string(b)), e
}
func (t tmux) capturePane(ctx context.Context, pane string, lines int) ([]byte, error) {
	// The caller validates the exact stable target and the finite line bound.
	args := []string{"capture-pane", "-p", "-e", "-t", pane, "-S", fmt.Sprint(-lines + 1)}
	argv := append([]string{"-L", t.socket}, args...)
	if t.socket == "cockpit" && !t.allowLiveCockpit {
		return nil, derr("INTERNAL", "live cockpit socket refused")
	}
	if t.trace != "" {
		if f, e := os.OpenFile(t.trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); e == nil {
			_, _ = fmt.Fprintf(f, "tmux %s\n", strings.Join(argv, " "))
			_ = f.Close()
		}
	}
	c := exec.CommandContext(ctx, "tmux", argv...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, e := c.Output()
	if e != nil {
		if ctx.Err() != nil {
			return nil, derr("CANCELLED", "capture cancelled")
		}
		return nil, fmt.Errorf("tmux %q: %w: %s", args, e, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// interact is intentionally not a general send-keys wrapper.  The only
// values accepted here are controller-selected typed actions and already
// validated literal instruction text; no MCP or CLI request can supply keys,
// a command, an environment, or a tmux target.
func (t tmux) interact(pane, action, text string) error {
	switch action {
	case "nudge", "resume":
		// Codex's TUI must receive its literal text and submit key as distinct
		// tmux client invocations. Codex materializes literal paste
		// asynchronously, so the bounded settle prevents C-m from overtaking
		// it. C-m is the terminal return key, not client-controlled raw key
		// input.
		if _, e := t.runSensitive("tmux -L "+t.socket+" interaction."+action+" text=[REDACTED]", "interaction."+action, "send-keys", "-t", pane, "-l", text); e != nil {
			return e
		}
		time.Sleep(literalSubmitSettle)
		_, e := t.runUntraced("interaction."+action, "send-keys", "-t", pane, "C-m")
		return e
	case "pause":
		_, e := t.runSensitive("tmux -L "+t.socket+" interaction."+action+" text=[REDACTED]", "interaction."+action, "send-keys", "-t", pane, "C-c")
		return e
	case "compact":
		_, e := t.runSensitive("tmux -L "+t.socket+" interaction."+action+" text=[REDACTED]", "interaction."+action, "send-keys", "-t", pane, "-l", "/compact", ";", "send-keys", "-t", pane, "Enter")
		return e
	default:
		return derr("INTERNAL", "unknown typed interaction")
	}
}
func (t tmux) globalOption(key string) (string, error) {
	b, e := t.run("show-option", "-gqv", key)
	return strings.TrimSpace(string(b)), e
}
func (t tmux) serverSocketPath() (string, error) {
	b, e := t.run("display-message", "-p", "#{socket_path}")
	if e != nil {
		return "", e
	}
	p := strings.TrimSpace(string(b))
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("tmux did not return an absolute socket path")
	}
	return p, nil
}
func tracePath(root string) string { return filepath.Join(root, "driver.trace") }
