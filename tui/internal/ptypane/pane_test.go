package ptypane

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPane_WriteAndRenderPlainText(t *testing.T) {
	p := New(40, 5, nil)
	p.Write([]byte("hello world"))
	out := p.View()
	if !strings.Contains(out, "hello world") {
		t.Fatalf("expected 'hello world' in view, got %q", out)
	}
}

func TestPane_SGREscapesSurviveView(t *testing.T) {
	// CSI 31m = red, CSI 1m = bold, CSI 0m = reset. The vt emulator
	// should absorb these into cell attributes and re-emit them in
	// View()'s SGR-attributed output.
	p := New(40, 5, nil)
	p.Write([]byte("\x1b[31;1mfocus\x1b[0m rest"))
	out := p.View()
	if !strings.Contains(out, "focus") || !strings.Contains(out, "rest") {
		t.Fatalf("text missing from view: %q", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected SGR escapes to survive View(): %q", out)
	}
}

func TestPane_CursorMovesAreAbsorbed(t *testing.T) {
	// CSI cursor-positioning sequences (CUP) must be absorbed into
	// the emulator's screen state, NOT leak into View() output —
	// they would fight Bubble Tea's parent renderer if they did.
	p := New(40, 5, nil)
	p.Write([]byte("first line\r\n"))
	p.Write([]byte("\x1b[1;1H")) // CUP back to (1,1)
	p.Write([]byte("OVERWRITE"))
	out := p.View()
	if !strings.Contains(out, "OVERWRITE") {
		t.Fatalf("expected overwritten content: %q", out)
	}
	for _, esc := range []string{"\x1b[1;1H", "\x1b[H", "\x1b[2;3H"} {
		if strings.Contains(out, esc) {
			t.Fatalf("CUP escape %q leaked into View(): %q", esc, out)
		}
	}
}

func TestPane_ResizeUpdatesDimensions(t *testing.T) {
	p := New(40, 5, nil)
	p.Resize(80, 24)
	w, h := p.Size()
	if w != 80 || h != 24 {
		t.Fatalf("Size() after Resize(80,24) = (%d,%d), want (80,24)", w, h)
	}
}

func TestPane_ResizeBelowMinimumIsClamped(t *testing.T) {
	// Tiny dimensions cause the emulator to misbehave — we clamp
	// silently so no caller has to defensively check.
	p := New(40, 5, nil)
	p.Resize(2, 1)
	w, h := p.Size()
	if w < 10 || h < 5 {
		t.Fatalf("dimensions below floor: got (%d,%d), expected >= (10,5)", w, h)
	}
}

func TestPane_PlainTextLinesIncludesScrollback(t *testing.T) {
	// Force enough output to push earlier lines into scrollback.
	p := New(40, 3, nil)
	for i, line := range []string{"alpha", "bravo", "charlie", "delta"} {
		_ = i
		p.Write([]byte(line + "\r\n"))
	}
	lines := p.PlainTextLines()
	joined := strings.Join(lines, "|")
	for _, want := range []string{"alpha", "bravo", "charlie", "delta"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("PlainTextLines should include scrollback line %q; got %q", want, joined)
		}
	}
}

func TestPane_PlainTextLinesEmptyWhenAltScreen(t *testing.T) {
	// In alt-screen mode the child has its own UI; scrollback is
	// suppressed. Search must report no matches there rather than
	// returning stale main-screen lines.
	p := New(40, 5, nil)
	p.Write([]byte("hello\r\n"))
	p.Write([]byte("\x1b[?1049h"))
	if got := p.PlainTextLines(); len(got) != 0 {
		t.Fatalf("PlainTextLines in alt-screen should be empty, got %v", got)
	}
}

func TestPane_AltScreenDetected(t *testing.T) {
	p := New(40, 5, nil)
	if p.IsAltScreen() {
		t.Fatalf("fresh pane should not be in alt-screen")
	}
	p.Write([]byte("\x1b[?1049h"))
	if !p.IsAltScreen() {
		t.Fatalf("expected alt-screen mode after CSI ?1049h")
	}
	p.Write([]byte("\x1b[?1049l"))
	if p.IsAltScreen() {
		t.Fatalf("expected primary screen after CSI ?1049l")
	}
}

func TestKeyToBytes_RunesPassThrough(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")}
	if got := string(KeyToBytes(msg)); got != "abc" {
		t.Fatalf("KeyToBytes(runes) = %q, want %q", got, "abc")
	}
}

func TestKeyToBytes_EnterIsCarriageReturn(t *testing.T) {
	// Claude expects \r (not \n) to submit a prompt — same as a real
	// terminal in cooked mode. Get this wrong and "send message"
	// looks like it does nothing.
	got := KeyToBytes(tea.KeyMsg{Type: tea.KeyEnter})
	if string(got) != "\r" {
		t.Fatalf("Enter encoded as %q, want %q", got, "\r")
	}
}

func TestKeyToBytes_AltEnterIsEscCR(t *testing.T) {
	// Claude Code reads ESC+CR as "newline-in-prompt". Most terminals
	// emit ESC+\r for Option/Alt+Enter, which bubbletea surfaces as
	// KeyMsg{Type: KeyEnter, Alt: true}, stringifying to "alt+enter".
	// Both this and the rare "shift+enter" form must encode the same
	// way so multiline prompt entry works regardless of which modifier
	// the user (or their terminal) is configured to send.
	msg := tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
	if msg.String() != "alt+enter" {
		t.Fatalf(
			"sanity: KeyMsg{Enter, Alt:true}.String() = %q, expected alt+enter",
			msg.String(),
		)
	}
	got := KeyToBytes(msg)
	if string(got) != "\x1b\r" {
		t.Fatalf("alt+enter encoded as %q, want %q", got, "\x1b\r")
	}

	// Plain Enter must still submit (\r), not insert a newline. A
	// regression here would mean every prompt the user typed inserted
	// a newline instead of being sent.
	plain := KeyToBytes(tea.KeyMsg{Type: tea.KeyEnter})
	if string(plain) != "\r" {
		t.Fatalf("plain Enter must encode as \\r, got %q", plain)
	}
}

func TestKeyToBytes_Arrows(t *testing.T) {
	cases := map[tea.KeyType]string{
		tea.KeyUp:    "\x1b[A",
		tea.KeyDown:  "\x1b[B",
		tea.KeyRight: "\x1b[C",
		tea.KeyLeft:  "\x1b[D",
	}
	for kt, want := range cases {
		got := string(KeyToBytes(tea.KeyMsg{Type: kt}))
		if got != want {
			t.Fatalf("arrow %v encoded as %q, want %q", kt, got, want)
		}
	}
}

// TestKeyToBytes_ShiftTabIsBackTab: claude's permission-mode toggle
// is bound to Shift+Tab, which the terminal protocol encodes as
// "back tab" (CSI Z = ESC [ Z). Pre-fix this returned nil, so a
// Shift+Tab in chubby's conversation pane was silently dropped.
func TestKeyToBytes_ShiftTabIsBackTab(t *testing.T) {
	got := string(KeyToBytes(tea.KeyMsg{Type: tea.KeyShiftTab}))
	if got != "\x1b[Z" {
		t.Fatalf("Shift+Tab encoded as %q, want %q (CSI Z back-tab)", got, "\x1b[Z")
	}
}

// TestKeyToBytes_FunctionKeys: F1..F12 follow the xterm-modern
// convention. F1-F4 use SS3 (ESC O P/Q/R/S), F5+ use CSI N~. Without
// these, function-key presses would silently drop after reaching the
// PTY router.
func TestKeyToBytes_FunctionKeys(t *testing.T) {
	cases := map[tea.KeyType]string{
		tea.KeyF1:  "\x1bOP",
		tea.KeyF2:  "\x1bOQ",
		tea.KeyF3:  "\x1bOR",
		tea.KeyF4:  "\x1bOS",
		tea.KeyF5:  "\x1b[15~",
		tea.KeyF6:  "\x1b[17~",
		tea.KeyF7:  "\x1b[18~",
		tea.KeyF8:  "\x1b[19~",
		tea.KeyF9:  "\x1b[20~",
		tea.KeyF10: "\x1b[21~",
		tea.KeyF11: "\x1b[23~",
		tea.KeyF12: "\x1b[24~",
	}
	for kt, want := range cases {
		got := string(KeyToBytes(tea.KeyMsg{Type: kt}))
		if got != want {
			t.Fatalf("F-key %v encoded as %q, want %q", kt, got, want)
		}
	}
}

// TestKeyToBytes_AltRunePrefixesEsc: Alt+letter / Alt+symbol must
// arrive at claude as ESC + rune so meta-key chords (e.g. Alt+B for
// word-back in readline) work. Pre-fix the Alt flag was silently
// stripped.
func TestKeyToBytes_AltRunePrefixesEsc(t *testing.T) {
	got := string(KeyToBytes(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("b"),
		Alt:   true,
	}))
	if got != "\x1bb" {
		t.Fatalf("Alt+b encoded as %q, want %q (ESC+b)", got, "\x1bb")
	}
}

func TestKeyToBytes_BackspaceIsDEL(t *testing.T) {
	// Modern terminals (and claude's input layer) expect DEL=0x7f
	// for Backspace, not BS=0x08. macOS's bash-in-Terminal uses 0x7f
	// — pin that contract.
	got := KeyToBytes(tea.KeyMsg{Type: tea.KeyBackspace})
	if len(got) != 1 || got[0] != 0x7f {
		t.Fatalf("Backspace encoded as %x, want 0x7f", got)
	}
}

func TestKeyToBytes_CtrlLetters(t *testing.T) {
	cases := map[tea.KeyType]byte{
		tea.KeyCtrlA: 0x01,
		tea.KeyCtrlC: 0x03,
		tea.KeyCtrlD: 0x04,
		tea.KeyCtrlZ: 0x1A,
	}
	for kt, want := range cases {
		got := KeyToBytes(tea.KeyMsg{Type: kt})
		if len(got) != 1 || got[0] != want {
			t.Fatalf("ctrl key %v encoded as %v, want 0x%02x", kt, got, want)
		}
	}
}

func TestKeyToBytes_UnsupportedReturnsNil(t *testing.T) {
	// Sanity that genuinely-unknown key types still drop to nil so
	// the caller can no-op rather than emit garbage. KeyNull is the
	// "no key" sentinel — never produced by a real keystroke.
	if KeyToBytes(tea.KeyMsg{Type: tea.KeyNull}) != nil {
		t.Fatalf("KeyNull should encode to nil")
	}
}
