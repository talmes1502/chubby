package views

import "testing"

func TestStripCursorEscapes_PreservesSGR(t *testing.T) {
	in := []byte("\x1b[31mred\x1b[0m plain")
	got := string(StripCursorEscapes(in))
	want := "\x1b[31mred\x1b[0m plain"
	if got != want {
		t.Fatalf("SGR was modified: got %q want %q", got, want)
	}
}

func TestStripCursorEscapes_DropsCursorMovement(t *testing.T) {
	cases := map[string]string{
		"a\x1b[3Ab":     "ab",     // up
		"a\x1b[2Bb":     "ab",     // down
		"a\x1b[2Cb":     "ab",     // right
		"a\x1b[5Db":     "ab",     // left
		"\x1b[Hhi":      "hi",     // home
		"\x1b[2;5Hhi":   "hi",     // CUP row;col
		"a\x1b[Kb":      "ab",     // erase line
		"a\x1b[2Jb":     "ab",     // erase display
		"\x1b[?2026lok": "ok",     // mode reset
		"\x1b[?2026hok": "ok",     // mode set
		"\x1b[?25lhide": "hide",   // hide cursor
		"\x1b[?2004lpaste": "paste", // bracketed paste off
	}
	for in, want := range cases {
		got := string(StripCursorEscapes([]byte(in)))
		if got != want {
			t.Errorf("input %q: got %q want %q", in, got, want)
		}
	}
}

func TestStripCursorEscapes_DropsOSC(t *testing.T) {
	cases := map[string]string{
		"x\x1b]0;Title\x07y":  "xy",
		"x\x1b]2;Title\x07y":  "xy",
		"x\x1b]0;Title\x1b\\y": "xy", // ST-terminated
	}
	for in, want := range cases {
		got := string(StripCursorEscapes([]byte(in)))
		if got != want {
			t.Errorf("input %q: got %q want %q", in, got, want)
		}
	}
}

func TestStripCursorEscapes_DropsEscPairs(t *testing.T) {
	in := []byte("a\x1b7b\x1b8c")
	got := string(StripCursorEscapes(in))
	want := "abc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStripCursorEscapes_PlainTextUntouched(t *testing.T) {
	in := []byte("hello world\nplain text")
	got := string(StripCursorEscapes(in))
	want := "hello world\nplain text"
	if got != want {
		t.Fatalf("plain text modified: got %q want %q", got, want)
	}
}

func TestStripCursorEscapes_EmptyInput(t *testing.T) {
	got := StripCursorEscapes(nil)
	if len(got) != 0 {
		t.Fatalf("nil input produced %q", got)
	}
	got = StripCursorEscapes([]byte{})
	if len(got) != 0 {
		t.Fatalf("empty input produced %q", got)
	}
}

func TestStripCursorEscapes_MixedSGRAndCursor(t *testing.T) {
	// Real-world snippet: hide cursor, color red, write text, restore color, move up.
	in := []byte("\x1b[?25l\x1b[31merror\x1b[0m\x1b[3A")
	got := string(StripCursorEscapes(in))
	want := "\x1b[31merror\x1b[0m"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStripCursorEscapes_KittyKeyboard(t *testing.T) {
	// Kitty keyboard protocol push/pop. These end in 'u' (not 'm') so should be stripped.
	in := []byte("a\x1b[>1ub\x1b[<uc")
	got := string(StripCursorEscapes(in))
	want := "abc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
