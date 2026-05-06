package views

import (
	"strings"
	"testing"
)

// containsAll asserts every needle appears in haystack. Returns the
// first missing needle (and true) when something's absent.
func containsAll(haystack string, needles ...string) (string, bool) {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return n, true
		}
	}
	return "", false
}

func TestRawStatusBar_ModeMainEmptyCompose(t *testing.T) {
	got := rawStatusBar(StatusModeMain, false, 0, false, false)
	if missing, ok := containsAll(got, "F6 → claude", "Ctrl+\\", "Ctrl+A", "Ctrl+B", "Ctrl+D", "Ctrl+H", "Ctrl+J", "Ctrl+N", "Ctrl+P", "Ctrl+K", "Ctrl+Y", "?", "q"); ok {
		t.Fatalf("ModeMain (empty compose, rail) missing %q: %s", missing, got)
	}
}

// TestRawStatusBar_ModeMainConversationPane: in the conversation pane
// claude owns the keyboard. Status bar should advertise the small
// set of chubby chords still live (F6, Ctrl+\, Ctrl+C) and NOT the
// rail-only chords (Ctrl+B/D/N/...) so users aren't confused into
// pressing them.
func TestRawStatusBar_ModeMainConversationPane(t *testing.T) {
	got := rawStatusBar(StatusModeMain, false, 0, false, true)
	if missing, ok := containsAll(got, "claude", "F6", "Ctrl+\\", "Ctrl+C"); ok {
		t.Fatalf("ModeMain (conversation pane) missing %q: %s", missing, got)
	}
	for _, n := range []string{"Ctrl+B", "Ctrl+N", "Ctrl+D×2", "Ctrl+R rename", "Ctrl+Y"} {
		if strings.Contains(got, n) {
			t.Fatalf("conversation pane status should not advertise rail-only chord %q: %s", n, got)
		}
	}
}

func TestRawStatusBar_ModeMainWithComposeText(t *testing.T) {
	got := rawStatusBar(StatusModeMain, true, 0, false, false)
	if missing, ok := containsAll(got, "Enter send", "Shift+Enter", "@name", "Tab complete", "Esc"); ok {
		t.Fatalf("ModeMain (compose=text) missing %q: %s", missing, got)
	}
	// Verify the empty-compose hints are NOT shown.
	if strings.Contains(got, "Ctrl+B") {
		t.Fatalf("compose-text status should not include Ctrl+B: %s", got)
	}
}

func TestRawStatusBar_ModeBroadcastField0(t *testing.T) {
	got := rawStatusBar(StatusModeBroadcast, false, 0, false, false)
	if missing, ok := containsAll(got, "Tab fields", "Space toggle", "all", "none", "invert", "Esc"); ok {
		t.Fatalf("ModeBroadcast field 0 missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeBroadcastField1(t *testing.T) {
	got := rawStatusBar(StatusModeBroadcast, false, 1, false, false)
	if missing, ok := containsAll(got, "Tab fields", "/cmd", "Esc"); ok {
		t.Fatalf("ModeBroadcast field 1 missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeBroadcastField2(t *testing.T) {
	got := rawStatusBar(StatusModeBroadcast, false, 2, false, false)
	if missing, ok := containsAll(got, "Tab fields", "Enter send", "selected", "Esc"); ok {
		t.Fatalf("ModeBroadcast field 2 missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeGrep(t *testing.T) {
	got := rawStatusBar(StatusModeGrep, false, 0, false, false)
	if missing, ok := containsAll(got, "navigate", "jump", "Esc"); ok {
		t.Fatalf("ModeGrep missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeHistory(t *testing.T) {
	got := rawStatusBar(StatusModeHistory, false, 0, false, false)
	if missing, ok := containsAll(got, "columns", "select", "open", "Esc"); ok {
		t.Fatalf("ModeHistory missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeSpawn(t *testing.T) {
	got := rawStatusBar(StatusModeSpawn, false, 0, false, false)
	if missing, ok := containsAll(got, "Tab", "Enter", "spawn", "Esc", "cancel"); ok {
		t.Fatalf("ModeSpawn missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeSearch(t *testing.T) {
	got := rawStatusBar(StatusModeSearch, false, 0, false, false)
	if missing, ok := containsAll(got, "filter", "Enter", "Esc"); ok {
		t.Fatalf("ModeSearch missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeHelp(t *testing.T) {
	got := rawStatusBar(StatusModeHelp, false, 0, false, false)
	if !strings.Contains(got, "any key") {
		t.Fatalf("ModeHelp should hint that any key dismisses: %s", got)
	}
}

func TestRawStatusBar_ModeReconnecting(t *testing.T) {
	got := rawStatusBar(StatusModeReconnecting, false, 0, false, false)
	if missing, ok := containsAll(got, "connecting", "chubbyd", "Ctrl+C"); ok {
		t.Fatalf("ModeReconnecting missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeAttach(t *testing.T) {
	got := rawStatusBar(StatusModeAttach, false, 0, false, false)
	if missing, ok := containsAll(got, "navigate", "Space toggle", "all", "none", "Enter attach", "rescan", "Esc"); ok {
		t.Fatalf("ModeAttach missing %q: %s", missing, got)
	}
}

func TestRawStatusBar_ModeAttachConfirm(t *testing.T) {
	got := rawStatusBar(StatusModeAttach, false, 0, true, false)
	if missing, ok := containsAll(got, "y", "confirm", "n", "cancel"); ok {
		t.Fatalf("ModeAttach (confirm) missing %q: %s", missing, got)
	}
	if strings.Contains(got, "Space toggle") {
		t.Fatalf("ModeAttach (confirm) should not show navigate hints: %s", got)
	}
}

func TestStatusBarText_TruncatesWithEllipsis(t *testing.T) {
	// Force truncation by passing a very small width.
	got := StatusBarText(StatusModeMain, false, 0, false, false, 12)
	if !strings.Contains(got, "…") {
		t.Fatalf("expected ellipsis when width=12, got %q", got)
	}
}

func TestStatusBarText_NoTruncationWhenWide(t *testing.T) {
	got := StatusBarText(StatusModeHelp, false, 0, false, false, 200)
	// Not truncated, but it IS wrapped in ANSI dim styling — strip-check
	// by verifying the underlying text is still substring-findable.
	if !strings.Contains(got, "any key") {
		t.Fatalf("wide width should preserve text, got %q", got)
	}
	if strings.Contains(got, "…") {
		t.Fatalf("wide width should not truncate, got %q", got)
	}
}

func TestTopStatus_NoRunID(t *testing.T) {
	got := TopStatus("", 5, 0, 80)
	if missing, ok := containsAll(got, "chubby", "5 sessions"); ok {
		t.Fatalf("TopStatus missing %q: %s", missing, got)
	}
	if strings.Contains(got, "idle") {
		t.Fatalf("TopStatus with idle=0 should omit idle suffix: %s", got)
	}
}

func TestTopStatus_WithIdleSuffix(t *testing.T) {
	got := TopStatus("", 5, 2, 80)
	if missing, ok := containsAll(got, "5 sessions", "2 idle", "⚡"); ok {
		t.Fatalf("TopStatus idle=2 missing %q: %s", missing, got)
	}
}

func TestTopStatus_WithRunID(t *testing.T) {
	got := TopStatus("ab12cd", 3, 0, 80)
	if missing, ok := containsAll(got, "chubby", "ab12cd", "3 sessions"); ok {
		t.Fatalf("TopStatus with runID missing %q: %s", missing, got)
	}
}

func TestTopStatus_TruncatesAtNarrowWidth(t *testing.T) {
	got := TopStatus("abcdef", 99, 5, 6)
	if !strings.Contains(got, "…") {
		t.Fatalf("expected ellipsis in narrow TopStatus, got %q", got)
	}
}

func TestTruncateWithEllipsis(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"abc", 1, "…"},
		{"", 5, ""},
		// Multi-byte separator survives boundary.
		{"a · b · c", 5, "a · …"},
	}
	for _, c := range cases {
		got := truncateWithEllipsis(c.in, c.width)
		if got != c.want {
			t.Errorf("truncate(%q, %d) = %q want %q", c.in, c.width, got, c.want)
		}
	}
}
