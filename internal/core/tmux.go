package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type tmux struct{ socket, trace string }

func (t tmux) run(args ...string) ([]byte, error) {
	argv := append([]string{"-L", t.socket}, args...)
	if t.socket == "cockpit" {
		return nil, derr("INTERNAL", "live cockpit socket refused")
	}
	if t.trace != "" {
		f, e := os.OpenFile(t.trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e == nil {
			_, _ = fmt.Fprintf(f, "tmux %s\n", strings.Join(argv, " "))
			_ = f.Close()
		}
	}
	c := exec.Command("tmux", argv...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, e := c.Output()
	if e != nil {
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
	if t.socket == "cockpit" {
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
	var args []string
	switch action {
	case "nudge", "resume":
		args = []string{"send-keys", "-t", pane, "-l", text, ";", "send-keys", "-t", pane, "Enter"}
	case "pause":
		args = []string{"send-keys", "-t", pane, "C-c"}
	case "compact":
		args = []string{"send-keys", "-t", pane, "-l", "/compact", ";", "send-keys", "-t", pane, "Enter"}
	default:
		return derr("INTERNAL", "unknown typed interaction")
	}
	_, e := t.run(args...)
	return e
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
