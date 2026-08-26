package main

import (
	"fmt"
	"strings"
)

// Every width calculation below runs on plain runes; SGR escapes are added only once a field has
// already been truncated and padded. Mixing the two is the classic way a bordered TUI ends up
// with ragged right edges the moment anything is colored.
func (m *model) draw(t *term) {
	th := m.th
	frame := fg(th.Frame)
	inner := t.w - 4 // │ + space … space + │
	if inner < 10 {
		inner = 10
	}

	filterLines := 0
	if m.mode == modeFilter || m.filter != "" {
		filterLines = 1
	}
	listH := t.h - 5 - filterLines
	if listH < 1 {
		listH = 1
	}
	m.scrollTo(listH)

	var b strings.Builder
	b.WriteString(cursorHome)

	// top border, with the title inlaid
	b.WriteString(frame + "╭─" + sgrReset)
	used := 2
	if m.title != "" {
		tt := truncate(m.title, inner-2)
		b.WriteString(" " + fg(th.Title) + sgrBold + tt + sgrReset + " ")
		used += runeLen(tt) + 2
	}
	if pad := t.w - used - 1; pad > 0 {
		b.WriteString(frame + strings.Repeat("─", pad) + "╮" + sgrReset)
	} else {
		b.WriteString(frame + "╮" + sgrReset)
	}
	b.WriteString(clearLine + "\r\n")

	if filterLines == 1 {
		shown := truncate(m.filter, inner-4)
		body := fg(th.Index) + "/" + sgrReset + " " + fg(th.Highlight) + shown + sgrReset
		bodyPlain := 2 + runeLen(shown)
		if m.mode == modeFilter {
			body += bg(th.SelBg) + " " + sgrReset // block cursor
			bodyPlain++
		}
		m.line(&b, frame, body, bodyPlain, inner)
	}

	m.line(&b, frame, "", 0, inner) // spacer

	if len(m.rows) == 0 {
		msg := fg(th.Dim) + truncate(m.empty, inner) + sgrReset
		m.line(&b, frame, msg, runeLen(truncate(m.empty, inner)), inner)
		for i := 1; i < listH; i++ {
			m.line(&b, frame, "", 0, inner)
		}
	} else if len(m.view) == 0 {
		msg := fg(th.Dim) + "no rows match " + sgrReset + fg(th.Highlight) + truncate(m.filter, 30) + sgrReset
		m.line(&b, frame, msg, 14+runeLen(truncate(m.filter, 30)), inner)
		for i := 1; i < listH; i++ {
			m.line(&b, frame, "", 0, inner)
		}
	} else {
		widths := m.columns(inner - 2)
		terms := strings.Fields(strings.ToLower(m.filter))
		for i := 0; i < listH; i++ {
			idx := m.off + i
			if idx >= len(m.view) {
				m.line(&b, frame, "", 0, inner)
				continue
			}
			body, plain := m.rowLine(m.rows[m.view[idx]], widths, idx == m.cur, terms, inner)
			m.line(&b, frame, body, plain, inner)
		}
	}

	m.line(&b, frame, "", 0, inner) // spacer

	legend, legendPlain := m.legend(inner)
	m.line(&b, frame, legend, legendPlain, inner)

	b.WriteString(frame + "╰" + strings.Repeat("─", t.w-2) + "╯" + sgrReset + clearLine)
	b.WriteString(clearBelow)
	t.write(b.String())
}

// line writes one bordered row. plainLen is the visible width of body, which the caller knows and
// the escape-laden string cannot report.
func (m *model) line(b *strings.Builder, frame, body string, plainLen, inner int) {
	b.WriteString(frame + "│" + sgrReset + " ")
	b.WriteString(body)
	if pad := inner - plainLen; pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	b.WriteString(" " + frame + "│" + sgrReset + clearLine + "\r\n")
}

func (m *model) scrollTo(listH int) {
	if m.cur < m.off {
		m.off = m.cur
	}
	if m.cur >= m.off+listH {
		m.off = m.cur - listH + 1
	}
	if m.off < 0 {
		m.off = 0
	}
}

// columns sizes each TSV field to its content, then takes width back off the widest column first
// until the row fits. Shrinking the widest keeps short, information-dense fields (an age, a pane
// count, an agent tag) intact and spends the loss on the long free-text one.
func (m *model) columns(avail int) []int {
	n := 0
	for _, vi := range m.view {
		if c := len(m.rows[vi].cells); c > n {
			n = c
		}
	}
	if n == 0 {
		return nil
	}
	w := make([]int, n)
	for _, vi := range m.view {
		for i, c := range m.rows[vi].cells {
			if l := runeLen(c); l > w[i] {
				w[i] = l
			}
		}
	}
	const gap = 2
	const floor = 6
	total := func() int {
		s := gap * (n - 1)
		for _, x := range w {
			s += x
		}
		return s
	}
	for total() > avail {
		widest, wi := 0, -1
		for i, x := range w {
			if x > widest && x > floor {
				widest, wi = x, i
			}
		}
		if wi < 0 {
			break
		}
		w[wi]--
	}
	return w
}

func (m *model) rowLine(r row, widths []int, selected bool, terms []string, inner int) (string, int) {
	th := m.th
	var b strings.Builder
	plain := 0

	if selected {
		b.WriteString(bg(th.SelBg) + fg(th.SelFg))
		b.WriteString("› ")
	} else {
		b.WriteString(fg(th.Index) + "  " + sgrReset)
	}
	plain += 2

	for i, w := range widths {
		if i > 0 {
			if selected {
				b.WriteString("  ")
			} else {
				b.WriteString("  ")
			}
			plain += 2
		}
		var txt string
		if i < len(r.cells) {
			txt = r.cells[i]
		}
		txt = pad(truncate(txt, w), w)
		plain += w
		if selected {
			b.WriteString(txt) // already inside the selection SGR pair
			continue
		}
		b.WriteString(colorize(txt, m.cellColor(i, txt), terms, th))
	}

	if selected {
		// extend the highlight bar to the full inner width
		if rest := inner - plain; rest > 0 {
			b.WriteString(strings.Repeat(" ", rest))
			plain += rest
		}
		b.WriteString(sgrReset)
	}
	return b.String(), plain
}

// cellColor is a heuristic, not a contract: the picker is generic, so it infers roles rather than
// being told them. First column reads as the name, anything path-shaped gets the path hue, the
// rest stay quiet.
func (m *model) cellColor(i int, txt string) string {
	s := strings.TrimSpace(txt)
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") {
		return fg(m.th.Path)
	}
	if i == 0 {
		return fg(m.th.Title)
	}
	return fg(m.th.Dim)
}

// colorize paints txt in base, lifting any filter-term match to the highlight color so it is
// obvious WHY a row survived the filter.
func colorize(txt, base string, terms []string, th theme) string {
	if len(terms) == 0 {
		return base + txt + sgrReset
	}
	rs := []rune(txt)
	mark := make([]bool, len(rs))
	low := []rune(strings.ToLower(txt))
	for _, t := range terms {
		tr := []rune(t)
		if len(tr) == 0 {
			continue
		}
		for i := 0; i+len(tr) <= len(low); i++ {
			hit := true
			for j := range tr {
				if low[i+j] != tr[j] {
					hit = false
					break
				}
			}
			if hit {
				for j := range tr {
					mark[i+j] = true
				}
			}
		}
	}
	var b strings.Builder
	cur := false
	b.WriteString(base)
	for i, r := range rs {
		if mark[i] != cur {
			cur = mark[i]
			if cur {
				b.WriteString(sgrReset + fg(th.Highlight) + sgrBold)
			} else {
				b.WriteString(sgrReset + base)
			}
		}
		b.WriteRune(r)
	}
	b.WriteString(sgrReset)
	return b.String()
}

// legend fits the key hints into inner columns. Hints are dropped from the right — the least
// important first — rather than wrapped: a legend that overflows pushes the whole frame up a line
// and scrolls the top border off, which looks like a rendering failure rather than a tight fit.
// The n/total counter is reserved for first, since it is the one part that changes as you type.
func (m *model) legend(inner int) (string, int) {
	th := m.th
	key := fg(th.Index)
	txt := fg(th.Dim)

	type hint struct{ k, d string }
	var hints []hint
	if m.mode == modeFilter {
		hints = []hint{{"type", "filter"}, {"⏎", "accept"}, {"esc", "clear"}}
	} else {
		hints = []hint{{"↑↓", "move"}, {"/", "filter"}, {"⏎", "select"}, {"esc", "cancel"}}
	}
	if m.footer != "" {
		hints = append(hints, hint{"", m.footer})
	}

	count := ""
	if len(m.rows) > 0 {
		count = fmt.Sprintf("%d/%d", len(m.view), len(m.rows))
	}

	budget := inner
	if count != "" {
		budget -= runeLen(count) + 3
	}

	var body strings.Builder
	plain := 0
	for _, h := range hints {
		label := h.d
		if h.k != "" {
			label = h.k + " " + h.d
		}
		width := runeLen(label)
		if plain > 0 {
			width += 3
		}
		if plain+width > budget {
			break
		}
		if plain > 0 {
			body.WriteString(txt + "   " + sgrReset)
		}
		if h.k != "" {
			body.WriteString(key + h.k + sgrReset + txt + " " + h.d + sgrReset)
		} else {
			body.WriteString(txt + h.d + sgrReset)
		}
		plain += width
	}

	if count != "" {
		if plain > 0 {
			body.WriteString(txt + "   " + sgrReset)
			plain += 3
		}
		body.WriteString(txt + count + sgrReset)
		plain += runeLen(count)
	}
	return body.String(), plain
}

func runeLen(s string) int { return len([]rune(s)) }

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

func pad(s string, w int) string {
	if d := w - runeLen(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
