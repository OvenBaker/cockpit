package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Palette slots mirror santa's Santa.Cli/Theming/Theme.cs one-for-one, so a cockpit popup and
// `santa tui` read as the same tool. Values are truecolor hex; the wire format is a 24-bit SGR
// escape, which tmux 3.5a passes through to the outer terminal. Deliberately duplicated rather
// than shared: the two tools have separate release cycles, and 30 lines of hex is a cheaper
// coupling than a package boundary between a C# CLI and a Go one.
type theme struct {
	Name string

	Index     string // row marker / ordinal
	Title     string // first display column
	Path      string // anything that looks like a filesystem path
	Dim       string // trailing columns, hints, counts
	Frame     string // border
	Highlight string // matched filter substring

	SelFg string
	SelBg string
}

var themes = map[string]theme{
	// cockpit's own chrome is already amber-on-slate (pane-active-border-style fg=colour220,
	// status bg=colour234), so slate-amber is the default: the popup border and the grid behind
	// it land on the same hue instead of clashing.
	"slate-amber": {
		Name:      "slate-amber",
		Index:     "#fbbf24",
		Title:     "#fcd34d",
		Path:      "#5eead4",
		Dim:       "#737373",
		Frame:     "#475569",
		Highlight: "#fde047",
		SelFg:     "#fef3c7",
		SelBg:     "#1f2937",
	},
	"tokyo": {
		Name:      "tokyo",
		Index:     "#7aa2f7",
		Title:     "#bb9af7",
		Path:      "#7dcfff",
		Dim:       "#565f89",
		Frame:     "#3b4261",
		Highlight: "#e0af68",
		SelFg:     "#c0caf5",
		SelBg:     "#283457",
	},
	"mono-teal": {
		Name:      "mono-teal",
		Index:     "#14b8a6",
		Title:     "#e5e5e5",
		Path:      "#a3a3a3",
		Dim:       "#525252",
		Frame:     "#404040",
		Highlight: "#5eead4",
		SelFg:     "#fafafa",
		SelBg:     "#1c1917",
	},
}

func themeByName(n string) theme {
	if t, ok := themes[n]; ok {
		return t
	}
	return themes["slate-amber"]
}

const (
	sgrReset = "\x1b[0m"
	sgrBold  = "\x1b[1m"
)

// fg/bg render a #rrggbb literal as a 24-bit SGR sequence. An unparseable value yields an empty
// string rather than a broken escape, so a typo degrades to default-colored text.
func fg(hex string) string { return sgr(hex, 38) }
func bg(hex string) string { return sgr(hex, 48) }

func sgr(hex string, base int) string {
	r, g, b, ok := parseHex(hex)
	if !ok {
		return ""
	}
	return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", base, r, g, b)
}

func parseHex(h string) (int, int, int, bool) {
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff, true
}
