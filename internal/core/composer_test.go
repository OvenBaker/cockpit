package core

import (
	"strings"
	"testing"
)

// Screens below reproduce what `capture-pane -e` actually emits for the two
// provider TUIs (verified against live panes): Codex renders its placeholder
// hint dim (SGR 2) after '›'; Claude renders '❯' with nothing after it when
// empty, and its chooser dialogs put '❯' before the highlighted option.

func TestComposerOccupiedClaude(t *testing.T) {
	empty := strings.Join([]string{
		"  some transcript output",
		"\x1b[38;5;244m──────────────\x1b[0m",
		"\x1b[1m❯\x1b[0m ",
		"\x1b[38;5;244m──────────────\x1b[0m",
		"  Opus 4.8 high /home/x 66k/1M (7%)",
	}, "\n")
	if occupied, _ := composerOccupied([]byte(empty), "claude"); occupied {
		t.Fatal("an empty claude composer was read as occupied")
	}
	draft := strings.Replace(empty, "\x1b[1m❯\x1b[0m ", "\x1b[1m❯\x1b[0m half-typed operator promp", 1)
	if occupied, _ := composerOccupied([]byte(draft), "claude"); !occupied {
		t.Fatal("a claude draft was not detected")
	}
	// An open chooser puts '❯' before the highlighted option; C-m would confirm
	// it, so it must read as occupied.
	dialog := strings.Join([]string{
		"  resuming from a summary.",
		"  ❯ 1. Resume from summary (recommended)",
		"    2. Resume full session as-is",
		"  Enter to confirm · Esc to cancel",
	}, "\n")
	if occupied, _ := composerOccupied([]byte(dialog), "claude"); !occupied {
		t.Fatal("an open chooser dialog was not detected")
	}
}

func TestComposerOccupiedCodexDimPlaceholderIsEmpty(t *testing.T) {
	// Verbatim shape from a live pane: dim '›' in a notice line above, then the
	// composer with a dim placeholder, then the status footer.
	placeholder := strings.Join([]string{
		"\x1b[1;2m› \x1b[0mCockpit restarted, but some panes had failures:",
		"",
		"\x1b[1m›\x1b[0m \x1b[2mExplain this codebase\x1b[0m",
		"",
		"  gpt-5.6-sol high · ~ · Context 71% left",
	}, "\n")
	if occupied, _ := composerOccupied([]byte(placeholder), "codex"); occupied {
		t.Fatal("a dim codex placeholder was read as a draft")
	}
	draft := strings.Replace(placeholder, "\x1b[1m›\x1b[0m \x1b[2mExplain this codebase\x1b[0m",
		"\x1b[1m›\x1b[0m fix the flaky test in", 1)
	if occupied, _ := composerOccupied([]byte(draft), "codex"); !occupied {
		t.Fatal("a codex draft was not detected")
	}
}

func TestComposerOccupiedFailsOpen(t *testing.T) {
	blank := []byte("plain shell output\nno prompt glyph anywhere\n")
	if occupied, _ := composerOccupied(blank, "claude"); occupied {
		t.Fatal("a screen with no composer must fail open")
	}
	if occupied, _ := composerOccupied([]byte("❯ text"), "unknown-provider"); occupied {
		t.Fatal("an unsupported provider must fail open")
	}
}

func TestVisibleAfterMarkerTracksExtendedColour(t *testing.T) {
	// 38;5;2 is palette green, NOT the dim attribute — its '2' must not hide the
	// draft that follows.
	line := "❯ \x1b[38;5;2mgreen draft\x1b[0m"
	got, found := visibleAfterMarker(line, '❯')
	if !found || strings.TrimSpace(got) != "green draft" {
		t.Fatalf("extended colour swallowed the draft: %q found=%v", got, found)
	}
	// ...while a genuine dim span stays invisible even mid-line.
	hint := "❯ \x1b[2mplaceholder hint\x1b[0m"
	if got, _ = visibleAfterMarker(hint, '❯'); strings.TrimSpace(got) != "" {
		t.Fatalf("dim placeholder leaked into the draft: %q", got)
	}
}
