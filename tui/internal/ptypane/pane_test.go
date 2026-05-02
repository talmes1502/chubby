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
	// F-keys aren't covered yet — they should silently return nil
	// so the caller can decide how to handle (drop / log / pass to
	// chubby-level handler).
	if KeyToBytes(tea.KeyMsg{Type: tea.KeyF1}) != nil {
		t.Fatalf("F1 unexpectedly returned bytes; update test if you've added it")
	}
}
