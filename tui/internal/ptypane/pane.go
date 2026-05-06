// Package ptypane wraps a charmbracelet/x/vt emulator into a
// Bubble-Tea-friendly view primitive. Instances are owned by the
// model (one per session), fed PTY chunks via Write, and produce a
// renderable string via View.
//
// Design choices:
//
//   - Emulator state is per-session; switching focus does NOT reset
//     the emulator, so when the user flips back the previous screen
//     is still there.
//   - Render returns an SGR-attributed string (no cursor-positioning
//     escapes — those would fight Bubble Tea's parent renderer).
//     vt.Emulator.Render() already does this; we just frame it.
//   - Keystrokes are encoded by KeyToBytes, which the model writes
//     to the daemon's inject RPC. The PTY end of the round trip
//     lives entirely in the wrapper — chubby-tui never opens a PTY.
//
// Concurrency: Write may be called from a goroutine reading the
// daemon's pty_chunk event stream. View / Resize / IsAltScreen are
// called from Bubble Tea's Update goroutine. vt.Emulator is
// thread-safe under SafeEmulator; we use the plain Emulator behind
// a mutex because the safe variant has a heavier API surface and we
// only need a few methods.
package ptypane

import (
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/vt"
)

// Responder is the callback the Pane invokes when the underlying vt
// emulator emits bytes that should be sent BACK to the wrapped child
// (claude). claude sometimes asks the terminal for information —
// Device-Attributes (DA1), cursor-position-report (DSR), Decimal-mode
// queries — and waits for a response. The caller (Model) wires this
// to inject_raw so claude's PTY receives the response and unblocks.
//
// Without a Responder, vt.Emulator.Write deadlocks: vt buffers the
// response in an internal pipe, the pipe fills up, the next Write
// blocks waiting for a reader.
type Responder func(bs []byte)

// Pane wraps one session's vt emulator. Construct with New(w, h, responder).
type Pane struct {
	mu   sync.Mutex
	em   *vt.Emulator
	w    int
	h    int
	resp Responder
	done chan struct{}
}

// New creates a pane sized to (w, h). Min 10x5 — below that the
// emulator behaves erratically and there's nothing useful to render
// anyway. responder may be nil for tests that don't exercise the
// response path; production callers must pass one or risk a deadlock
// the moment claude sends a query (DA1 / cursor-position / etc.).
func New(w, h int, responder Responder) *Pane {
	if w < 10 {
		w = 10
	}
	if h < 5 {
		h = 5
	}
	em := vt.NewEmulator(w, h)
	em.Focus()
	p := &Pane{
		em:   em,
		w:    w,
		h:    h,
		resp: responder,
		done: make(chan struct{}),
	}
	go p.drainResponses()
	return p
}

// drainResponses pulls bytes the emulator wants to send back to its
// child (DA1 replies, cursor-position reports, etc.) and forwards
// them via the responder callback. Without this, vt.Emulator.Write
// deadlocks waiting for the response pipe to be read.
func (p *Pane) drainResponses() {
	buf := make([]byte, 4096)
	for {
		select {
		case <-p.done:
			return
		default:
		}
		n, err := p.em.Read(buf)
		if n > 0 && p.resp != nil {
			out := make([]byte, n)
			copy(out, buf[:n])
			p.resp(out)
		}
		if err != nil {
			return
		}
	}
}

// Close shuts down the response-drain goroutine. Idempotent. Tests
// should defer Close so they don't leak goroutines.
func (p *Pane) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return nil
	default:
		close(p.done)
		return p.em.Close()
	}
}

// Write feeds a PTY chunk into the emulator. Safe to call from any
// goroutine.
func (p *Pane) Write(chunk []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.em.Write(chunk)
}

// Resize updates both the dimensions cached on Pane and the emulator's
// internal grid. The caller is responsible for separately notifying
// the daemon so the wrapper can SIGWINCH the wrapped claude (the
// emulator's grid and the child PTY's grid must stay in sync).
func (p *Pane) Resize(w, h int) {
	if w < 10 {
		w = 10
	}
	if h < 5 {
		h = 5
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if w == p.w && h == p.h {
		return
	}
	p.w, p.h = w, h
	p.em.Resize(w, h)
}

// View returns the emulator's current screen as a styled string,
// suitable for embedding inside a Bubble Tea view. Bubble Tea's
// renderer treats the output as scanlines and passes SGR escapes
// through verbatim — vt's Render() already strips cursor-positioning
// escapes (they're absorbed into screen state), so the result composes
// with the parent frame without fighting.
func (p *Pane) View() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.em.Render()
}

// IsAltScreen reports whether the emulator is currently in alt-screen
// mode. Useful for UI hints (e.g., disabling chubby's per-line scroll
// when the child has switched to a full-screen TUI of its own).
func (p *Pane) IsAltScreen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.em.IsAltScreen()
}

// PlainTextLines returns the scrollback + visible screen as plain
// text, one string per terminal row, oldest first. Trailing whitespace
// per line is stripped. Powers in-pane search (Ctrl+F): given the
// returned slice, callers can grep with a substring filter and
// navigate matches by line index.
//
// Returns an empty slice when the emulator is in alt-screen mode (the
// child has taken over with its own full-screen UI; scrollback is
// suppressed there per vt's contract).
func (p *Pane) PlainTextLines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.em.IsAltScreen() {
		return nil
	}
	out := make([]string, 0, p.em.ScrollbackLen()+p.h)
	// Scrollback first (oldest-first), then the visible screen.
	for y := 0; y < p.em.ScrollbackLen(); y++ {
		out = append(out, plainLineFromCells(func(x int) string {
			c := p.em.ScrollbackCellAt(x, y)
			if c == nil {
				return ""
			}
			return c.Content
		}, p.w))
	}
	for y := 0; y < p.h; y++ {
		out = append(out, plainLineFromCells(func(x int) string {
			c := p.em.CellAt(x, y)
			if c == nil {
				return ""
			}
			return c.Content
		}, p.w))
	}
	return out
}

// plainLineFromCells assembles one row by joining cell contents and
// trimming trailing whitespace. Cells that report empty content (nil
// pointer or zero-width filler from a wide grapheme) are skipped so
// CJK / emoji rows don't gain stray spaces.
func plainLineFromCells(at func(int) string, w int) string {
	var b strings.Builder
	for x := 0; x < w; x++ {
		s := at(x)
		if s == "" {
			continue
		}
		b.WriteString(s)
	}
	return strings.TrimRight(b.String(), " \t")
}

// Size returns the cached (w, h) of the pane. Constant between
// Resize calls.
func (p *Pane) Size() (w, h int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.w, p.h
}

// KeyToBytes encodes a Bubble Tea KeyMsg into the byte sequence a
// real terminal would have sent for that key. Returns nil for keys
// we don't yet handle — the caller should drop those silently and
// add them as gaps surface, rather than guessing.
//
// Keys covered:
//   - rune input (letters, digits, punctuation, multi-byte unicode)
//   - Enter (\r, the convention claude uses for "submit"), Backspace
//     (DEL=0x7f, the convention most modern terminals use), Tab, Esc,
//     Space
//   - arrows (CSI A/B/C/D)
//   - Home/End/PgUp/PgDn/Delete (CSI H/F/5~/6~/3~)
//   - Ctrl-letter combos (Ctrl+A..Z → bytes 0x01..0x1A)
//   - Shift+Tab (CSI Z, "back tab" — claude's permission-mode toggle)
//   - Function keys F1..F12 (xterm-style sequences)
//   - Alt-modifier on KeyRunes (ESC + rune; standard meta encoding)
//
// Encoding sources: Shift+Tab is the xterm-defined "back tab"
// (CSI Z); F-keys follow the xterm modern set (F1-F4 = SS3-PQRS,
// F5+ = CSI N~). Alt+rune is the meta-prefix convention every
// readline-based REPL understands.
func KeyToBytes(msg tea.KeyMsg) []byte {
	// Alt+rune: ESC + rune bytes. Has to come before the bare-rune
	// branch below so the meta prefix gets prepended.
	if msg.Alt && msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		return append([]byte{0x1b}, []byte(string(msg.Runes))...)
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		return []byte(string(msg.Runes))
	}
	// "Newline-in-prompt" for Claude Code (and most modern REPLs)
	// is ESC+CR. Terminals emit this byte sequence for either
	// Shift+Enter or Option/Alt+Enter depending on emulator config;
	// Bubble Tea v1.x surfaces it as ``alt+enter`` (the common case —
	// terminals encode any Alt+key as ESC+key) and rarely
	// ``shift+enter`` (only on terminals with explicit shift-modifier
	// reporting enabled). Match both string forms so multiline-prompt
	// works on every reasonable terminal config.
	switch msg.String() {
	case "alt+enter", "shift+enter":
		return []byte{0x1b, '\r'}
	}
	switch msg.Type {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyShiftTab:
		// CSI Z (back tab). What claude reads to toggle permission
		// modes; without this branch a Shift+Tab in the conversation
		// pane was silently dropped.
		return []byte("\x1b[Z")
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyF1:
		return []byte("\x1bOP")
	case tea.KeyF2:
		return []byte("\x1bOQ")
	case tea.KeyF3:
		return []byte("\x1bOR")
	case tea.KeyF4:
		return []byte("\x1bOS")
	case tea.KeyF5:
		return []byte("\x1b[15~")
	case tea.KeyF6:
		// Note: chubby intercepts F6 in handleKeyConversation /
		// handleKeyRail, so this branch is only reached if the user
		// has remapped F6's intercept away. Encoding included for
		// completeness.
		return []byte("\x1b[17~")
	case tea.KeyF7:
		return []byte("\x1b[18~")
	case tea.KeyF8:
		return []byte("\x1b[19~")
	case tea.KeyF9:
		return []byte("\x1b[20~")
	case tea.KeyF10:
		return []byte("\x1b[21~")
	case tea.KeyF11:
		return []byte("\x1b[23~")
	case tea.KeyF12:
		return []byte("\x1b[24~")
	}
	if msg.Type >= tea.KeyCtrlA && msg.Type <= tea.KeyCtrlZ {
		return []byte{byte(msg.Type-tea.KeyCtrlA) + 1}
	}
	return nil
}
