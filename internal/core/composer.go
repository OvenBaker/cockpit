package core

// Typed-interaction guards. A nudge/resume/compact types literal text into the
// provider's composer and then submits with C-m — so whatever already sits in
// that composer is submitted too. Neither the hook chain nor the poller can see
// a human draft (no hook fires on operator keystrokes), so `waiting` alone can
// never prove the composer is empty. These guards decide the two facts that
// live outside the state machine, immediately before the effect:
//
//   - operatorEngaged: a human is at this pane right now (it is the focused
//     pane of an attached client with recent input). A machine wake of a pane
//     under live human control is never useful — the human is already there —
//     and it races their next keystroke.
//   - composerOccupied: the rendered screen shows draft text after the
//     provider's prompt glyph, or a chooser dialog is open (both providers
//     render '❯'/'›' before the highlighted option, and C-m would CONFIRM it).
//
// Both refusals are strictly pre-effect named degraded states: nothing has been
// typed, so a caller may safely retry them under the same idempotency key.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// How recently an attached, focused client must have sent input to count as an
// operator at the keyboard. Deliberately shorter than the callers' own retry
// cadences so a refusal defers a notice rather than starving it.
const operatorActivityWindow = 60 * time.Second

// Capture depth for the composer check. The composer sits on the last screen
// rows; this only needs to cover a tall draft plus the provider footer.
const composerCaptureLines = 45

var composerMarker = map[string]rune{"claude": '❯', "codex": '›'}

// operatorEngaged reports whether the target pane is the focused pane of an
// attached client whose input activity is inside the window. Unattached
// sessions and unfocused panes are never engaged.
func (d *daemon) operatorEngaged(paneID string) (bool, string) {
	b, err := d.tm.run("display-message", "-p", "-t", paneID, "#{session_attached}\t#{window_active}\t#{pane_active}\t#{session_id}")
	if err != nil {
		return false, ""
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\t")
	if len(f) != 4 || f[0] == "0" || f[0] == "" || f[1] != "1" || f[2] != "1" {
		return false, ""
	}
	c, err := d.tm.run("list-clients", "-t", f[3], "-F", "#{client_activity}")
	if err != nil {
		return false, ""
	}
	for _, field := range strings.Fields(string(c)) {
		sec, e := strconv.ParseInt(field, 10, 64)
		if e != nil {
			continue
		}
		if idle := time.Since(time.Unix(sec, 0)); idle >= 0 && idle < operatorActivityWindow {
			return true, fmt.Sprintf("an operator is focused on this pane and sent input %ds ago; a typed interaction would race their keystrokes", int(idle.Seconds()))
		}
	}
	return false, ""
}

// composerGuard runs the screen-derived half of the guard for one pane. An
// unreadable capture or an unrecognisable screen fails OPEN — this is a guard
// on a hazard only the screen can show, not a new gate on delivery.
func (d *daemon) composerGuard(paneID, provider string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	screen, err := d.tm.capturePane(ctx, paneID, composerCaptureLines)
	if err != nil {
		return false, ""
	}
	return composerOccupied(screen, provider)
}

// composerOccupied decides, from a pane's rendered screen, whether the
// provider's input line already holds text. The composer is the LAST line
// carrying the provider's prompt glyph; everything visible after the glyph is
// the draft — except dim (SGR 2) spans, which both TUIs use for placeholder
// hints an empty composer is allowed to show. A chooser dialog also puts the
// glyph before its highlighted option, so an open dialog reads as occupied —
// exactly right, since the submit key would confirm it.
func composerOccupied(screen []byte, provider string) (bool, string) {
	marker, ok := composerMarker[provider]
	if !ok {
		return false, ""
	}
	lines := strings.Split(string(screen), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		draft, found := visibleAfterMarker(lines[i], marker)
		if !found {
			continue
		}
		if strings.TrimSpace(draft) != "" {
			return true, "the pane's composer holds operator text or an open chooser; a typed interaction would submit it"
		}
		return false, ""
	}
	return false, ""
}

// visibleAfterMarker walks one raw captured line, tracking SGR dim state, and
// collects the non-dim visible text after the first occurrence of marker.
func visibleAfterMarker(line string, marker rune) (string, bool) {
	var out []rune
	dim, after := false, false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0x1b {
			if i+1 < len(runes) && runes[i+1] == '[' {
				j := i + 2
				for j < len(runes) && (runes[j] == ';' || runes[j] == ':' || (runes[j] >= '0' && runes[j] <= '9')) {
					j++
				}
				if j < len(runes) && runes[j] == 'm' {
					params := strings.Split(string(runes[i+2:j]), ";")
					for k := 0; k < len(params); k++ {
						switch params[k] {
						case "", "0", "22":
							dim = false
						case "2":
							dim = true
						case "38", "48":
							// Extended colour: consume its arguments so a
							// palette index is never read as an attribute.
							if k+1 < len(params) && params[k+1] == "5" {
								k += 2
							} else if k+1 < len(params) && params[k+1] == "2" {
								k += 4
							}
						}
					}
				}
				i = j
			}
			continue
		}
		if !after {
			if r == marker {
				after = true
			}
			continue
		}
		if !dim {
			out = append(out, r)
		}
	}
	return string(out), after
}
