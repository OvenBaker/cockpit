package main

import (
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"
)

// The UI is drawn on /dev/tty, never on stdout: inside `display-popup -E` the popup IS the pty,
// and the caller captures our stdout through $( ). Keeping the two apart is what lets the picker
// paint a full-screen frame and still hand back a clean machine-readable line — the same split
// cockpit-pick's startup mode already documents.
type term struct {
	f   *os.File
	old unix.Termios
	w   int
	h   int
}

func openTerm() (*term, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	t := &term{f: f}
	old, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		f.Close()
		return nil, err
	}
	t.old = *old

	raw := *old
	// cfmakeraw, minus OPOST: we emit our own \r\n, and leaving output processing off keeps
	// column arithmetic honest.
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, &raw); err != nil {
		f.Close()
		return nil, err
	}

	t.measure()
	return t, nil
}

func (t *term) measure() {
	t.w, t.h = 80, 24
	if ws, err := unix.IoctlGetWinsize(int(t.f.Fd()), unix.TIOCGWINSZ); err == nil {
		if ws.Col > 0 {
			t.w = int(ws.Col)
		}
		if ws.Row > 0 {
			t.h = int(ws.Row)
		}
	}
}

// restore is idempotent and safe to call from a deferred function during a panic; a picker that
// dies without putting the terminal back leaves the operator's popup in raw mode with no echo.
func (t *term) restore() {
	if t.f == nil {
		return
	}
	t.write(showCursor + exitAltScreen)
	_ = unix.IoctlSetTermios(int(t.f.Fd()), unix.TCSETS, &t.old)
	t.f.Close()
	t.f = nil
}

func (t *term) write(s string) { _, _ = t.f.WriteString(s) }

const (
	enterAltScreen = "\x1b[?1049h"
	exitAltScreen  = "\x1b[?1049l"
	hideCursor     = "\x1b[?25l"
	showCursor     = "\x1b[?25h"
	cursorHome     = "\x1b[H"
	clearLine      = "\x1b[K"
	clearBelow     = "\x1b[J"
)

func (t *term) onResize() <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, unix.SIGWINCH)
	return ch
}

// --- key decoding -----------------------------------------------------------

type keyKind int

const (
	keyRune keyKind = iota
	keyUp
	keyDown
	keyPgUp
	keyPgDn
	keyHome
	keyEnd
	keyEnter
	keyEsc
	keyBackspace
	keyCtrlC
	keyIgnore
)

type keyEvent struct {
	kind keyKind
	r    rune
}

// bytes arrive on a channel so the ESC/CSI disambiguation below can use a timeout: a lone ESC
// (cancel) and the ESC that opens an arrow-key sequence are the same first byte. tmux delivers
// the remainder of a real sequence in the same write, so a short window separates them reliably
// without the terminal-specific guesswork that makes hand-rolled key parsing fragile.
func readKeys(t *term) <-chan keyEvent {
	raw := make(chan byte, 256)
	go func() {
		defer close(raw)
		buf := make([]byte, 64)
		for {
			n, err := t.f.Read(buf)
			if n > 0 {
				for _, b := range buf[:n] {
					raw <- b
				}
			}
			if err != nil {
				return
			}
		}
	}()

	out := make(chan keyEvent, 16)
	go func() {
		defer close(out)
		for b := range raw {
			out <- decode(b, raw)
		}
	}()
	return out
}

func decode(b byte, raw <-chan byte) keyEvent {
	switch b {
	case 0x03:
		return keyEvent{kind: keyCtrlC}
	case '\r', '\n':
		return keyEvent{kind: keyEnter}
	case 0x7f, 0x08:
		return keyEvent{kind: keyBackspace}
	case 0x1b:
		return decodeEsc(raw)
	}
	if b < 0x20 {
		return keyEvent{kind: keyIgnore}
	}
	return keyEvent{kind: keyRune, r: rune(b)}
}

func decodeEsc(raw <-chan byte) keyEvent {
	next, ok := peek(raw)
	if !ok {
		return keyEvent{kind: keyEsc} // lone ESC → cancel
	}
	if next != '[' && next != 'O' {
		return keyEvent{kind: keyEsc}
	}
	final, ok := peek(raw)
	if !ok {
		return keyEvent{kind: keyEsc}
	}
	switch final {
	case 'A':
		return keyEvent{kind: keyUp}
	case 'B':
		return keyEvent{kind: keyDown}
	case 'C', 'D':
		return keyEvent{kind: keyIgnore} // left/right unused
	case 'H':
		return keyEvent{kind: keyHome}
	case 'F':
		return keyEvent{kind: keyEnd}
	}
	// numeric form: ESC [ <n> ~
	if final >= '0' && final <= '9' {
		n := int(final - '0')
		for {
			c, ok := peek(raw)
			if !ok {
				return keyEvent{kind: keyIgnore}
			}
			if c == '~' {
				break
			}
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
				continue
			}
			return keyEvent{kind: keyIgnore}
		}
		switch n {
		case 1, 7:
			return keyEvent{kind: keyHome}
		case 4, 8:
			return keyEvent{kind: keyEnd}
		case 5:
			return keyEvent{kind: keyPgUp}
		case 6:
			return keyEvent{kind: keyPgDn}
		}
	}
	return keyEvent{kind: keyIgnore}
}

func peek(raw <-chan byte) (byte, bool) {
	select {
	case b, ok := <-raw:
		return b, ok
	case <-time.After(25 * time.Millisecond):
		return 0, false
	}
}
