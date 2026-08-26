// cockpit-select — a filterable list picker for cockpit's tmux popups.
//
//	… | cockpit-select --title "Move pane 2 → workspace" --hide 1
//
// Reads TSV rows on stdin, draws a list on /dev/tty, and prints the chosen row VERBATIM to
// stdout (exit 0). Cancelling prints nothing and exits 1. It performs no tmux calls, takes no
// pane id, and cannot tell a workspace from a directory — the caller formats rows, the caller
// makes the single mutation.
//
// That split is deliberate rather than minimal. ADR-001 makes cockpit-core the one process that
// drives `tmux -L cockpit`, and names "each caller still needs tmux credentials, expanding the
// mutation boundary" as the cost to avoid. A picker that only chooses never joins that boundary,
// so when pane.move / pane.spawn land on the control socket the calling script swaps one tmux
// call for one ctl call and this binary is untouched.
//
// Keys mirror `santa tui` (Santa.Cli/Tui/TuiApp.cs) so the two tools share one set of reflexes:
// ↑/↓ move, PgUp/PgDn ±10, Home/End, "/" enters filter mode, Enter commits, Esc cancels.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	exitChosen    = 0
	exitCancelled = 1
	exitUsage     = 2
)

type row struct {
	raw   string   // returned to the caller unmodified
	cells []string // what we draw, after --hide
	hay   string   // lowercased haystack for filtering
}

func main() {
	var (
		title     = flag.String("title", "", "header shown in the top border")
		hideSpec  = flag.String("hide", "", "comma-separated 1-based TSV fields to omit from display (still returned)")
		matchSpec = flag.String("match", "", "comma-separated 1-based TSV fields the filter searches (default: every displayed field)")
		themeArg  = flag.String("theme", "slate-amber", "slate-amber | tokyo | mono-teal")
		empty     = flag.String("empty", "nothing to choose from", "message shown when there are no rows")
		footer    = flag.String("footer", "", "extra hint appended to the key legend")
	)
	flag.Parse()

	hidden, err := parseFields(*hideSpec, "--hide")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cockpit-select:", err)
		os.Exit(exitUsage)
	}
	searchable, err := parseFields(*matchSpec, "--match")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cockpit-select:", err)
		os.Exit(exitUsage)
	}

	rows, err := readRows(os.Stdin, hidden, searchable)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cockpit-select:", err)
		os.Exit(exitUsage)
	}

	th := themeByName(*themeArg)

	t, err := openTerm()
	if err != nil {
		// No controlling tty (cron, a pipe, a test harness). Fail loudly rather than silently
		// picking something on the operator's behalf.
		fmt.Fprintln(os.Stderr, "cockpit-select: no tty:", err)
		os.Exit(exitUsage)
	}
	defer t.restore()

	m := &model{rows: rows, th: th, title: *title, empty: *empty, footer: *footer}
	chosen := m.run(t)
	t.restore()

	if chosen == nil {
		os.Exit(exitCancelled)
	}
	fmt.Println(chosen.raw)
}

// parseFields reads a "2,3" field spec. A nil map means "unset", which callers distinguish from
// an empty selection.
func parseFields(spec, flagName string) (map[int]bool, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	out := map[int]bool{}
	for _, f := range strings.Split(spec, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("%s: %q is not a 1-based field number", flagName, f)
		}
		out[n] = true
	}
	return out, nil
}

func readRows(f *os.File, hidden, searchable map[int]bool) ([]row, error) {
	var rows []row
	sc := bufio.NewScanner(f)
	// Titles and paths are routinely longer than the 64KB default token, and a truncated row
	// would silently return a corrupt value to the caller.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		cells := make([]string, 0, len(fields))
		var hay []string
		for i, f := range fields {
			f = strings.TrimSpace(f)
			// --match is stated in ORIGINAL field numbers, the same numbering as --hide, so a
			// caller can search a field it chose not to display. Unset means "whatever is shown":
			// without it, a decorative column ("3 panes") drags every row into a match for "a".
			if searchable == nil {
				if !hidden[i+1] {
					hay = append(hay, f)
				}
			} else if searchable[i+1] {
				hay = append(hay, f)
			}
			if hidden[i+1] {
				continue
			}
			cells = append(cells, f)
		}
		if len(cells) == 0 {
			cells = []string{""}
		}
		rows = append(rows, row{
			raw:   line,
			cells: cells,
			hay:   strings.ToLower(strings.Join(hay, " ")),
		})
	}
	return rows, sc.Err()
}
