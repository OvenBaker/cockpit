package main

import (
	"strings"
)

type mode int

const (
	modeBrowse mode = iota
	modeFilter
)

type model struct {
	rows   []row
	th     theme
	title  string
	empty  string
	footer string

	view   []int // indices into rows that survive the current filter
	cur    int   // cursor position within view
	off    int   // first visible view index (scroll offset)
	filter string
	mode   mode
}

func (m *model) run(t *term) *row {
	m.reflow()
	t.write(enterAltScreen + hideCursor)

	keys := readKeys(t)
	resize := t.onResize()

	m.draw(t)
	for {
		select {
		case <-resize:
			t.measure()
			m.draw(t)
		case k, ok := <-keys:
			if !ok {
				return nil
			}
			if done, chosen := m.handle(k); done {
				return chosen
			}
			m.draw(t)
		}
	}
}

// handle returns (finished, chosen). chosen is nil for a cancel.
func (m *model) handle(k keyEvent) (bool, *row) {
	// With nothing to show, the only meaningful actions are leaving.
	if len(m.rows) == 0 {
		switch k.kind {
		case keyEsc, keyCtrlC, keyEnter, keyRune:
			return true, nil
		}
		return false, nil
	}

	switch k.kind {
	case keyCtrlC:
		return true, nil

	case keyUp:
		m.move(-1)
	case keyDown:
		m.move(1)
	case keyPgUp:
		m.move(-10)
	case keyPgDn:
		m.move(10)
	case keyHome:
		m.cur = 0
	case keyEnd:
		m.cur = len(m.view) - 1

	case keyEnter:
		if m.mode == modeFilter {
			// Accept the filter and return to browsing — the narrowed list stays.
			m.mode = modeBrowse
			return false, nil
		}
		if len(m.view) == 0 {
			return false, nil
		}
		r := m.rows[m.view[m.cur]]
		return true, &r

	case keyEsc:
		if m.mode == modeFilter {
			// Esc in filter mode abandons the filter but keeps the picker open; only Esc from
			// browse mode cancels outright. Two escapes to leave a filtered list is the same
			// shape as santa's, and it stops a stray keystroke from discarding the whole action.
			m.filter = ""
			m.mode = modeBrowse
			m.reflow()
			return false, nil
		}
		return true, nil

	case keyBackspace:
		if m.mode == modeFilter && m.filter != "" {
			r := []rune(m.filter)
			m.filter = string(r[:len(r)-1])
			m.reflow()
		}

	case keyRune:
		if m.mode == modeBrowse {
			switch k.r {
			case '/':
				m.mode = modeFilter
				return false, nil
			case 'q':
				return true, nil
			case 'j':
				m.move(1)
			case 'k':
				m.move(-1)
			case 'g':
				m.cur = 0
			case 'G':
				m.cur = len(m.view) - 1
			}
			return false, nil
		}
		m.filter += string(k.r)
		m.reflow()
	}
	return false, nil
}

func (m *model) move(d int) {
	if len(m.view) == 0 {
		return
	}
	m.cur += d
	if m.cur < 0 {
		m.cur = 0
	}
	if m.cur > len(m.view)-1 {
		m.cur = len(m.view) - 1
	}
}

// reflow recomputes the visible subset. The cursor is kept on the row it was on where possible,
// so typing another character does not jump the selection somewhere unrelated.
func (m *model) reflow() {
	var keep int = -1
	if m.cur < len(m.view) {
		keep = m.view[m.cur]
	}
	q := strings.ToLower(strings.TrimSpace(m.filter))
	m.view = m.view[:0]
	for i, r := range m.rows {
		if q == "" || matches(r.hay, q) {
			m.view = append(m.view, i)
		}
	}
	m.cur = 0
	if keep >= 0 {
		for i, idx := range m.view {
			if idx == keep {
				m.cur = i
				break
			}
		}
	}
	m.off = 0
}

// matches requires every whitespace-separated term to appear, so "repo cock" finds
// ~/repos/cockpit without demanding the words be adjacent or ordered.
func matches(hay, q string) bool {
	for _, term := range strings.Fields(q) {
		if !strings.Contains(hay, term) {
			return false
		}
	}
	return true
}
