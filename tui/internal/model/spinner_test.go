package model

import (
	"strings"
	"testing"
)

// TestSpinnerTick_AdvancesFrameAndReticksWhileThinking checks the
// happy path: a thinking session keeps the spinner running.
func TestSpinnerTick_AdvancesFrameAndReticksWhileThinking(t *testing.T) {
	m := Model{
		conversation: map[string][]Turn{},
		sessions:     []Session{{ID: "s1", Status: "thinking"}},
	}
	before := m.spinnerFrame
	tm, cmd := m.Update(spinnerTickMsg{})
	mm := tm.(Model)
	if mm.spinnerFrame != before+1 {
		t.Fatalf("spinnerFrame: expected %d got %d", before+1, mm.spinnerFrame)
	}
	if cmd == nil {
		t.Fatalf("expected non-nil cmd to schedule the next tick while a session is thinking")
	}
	if !mm.spinnerRunning {
		t.Fatalf("spinnerRunning should be true while at least one session is thinking")
	}
}

// TestSpinnerTick_StopsWhenNoneThinking checks that the tick stops
// re-arming itself once every session is idle, so the TUI doesn't
// burn CPU spinning forever in the background.
func TestSpinnerTick_StopsWhenNoneThinking(t *testing.T) {
	m := Model{
		conversation:   map[string][]Turn{},
		sessions:       []Session{{ID: "s1", Status: "idle"}},
		spinnerRunning: true,
	}
	tm, cmd := m.Update(spinnerTickMsg{})
	mm := tm.(Model)
	if cmd != nil {
		t.Fatalf("expected nil cmd when nothing is thinking (tick should stop)")
	}
	if mm.spinnerRunning {
		t.Fatalf("spinnerRunning should be false after stopping")
	}
}

// TestStatusGlyph_ThinkingAnimates asserts the glyph cycles through
// the spinner frames so successive renders draw different braille dots.
func TestStatusGlyph_ThinkingAnimates(t *testing.T) {
	a := statusGlyph("thinking", 0)
	b := statusGlyph("thinking", 1)
	if a == b {
		t.Fatalf("expected adjacent thinking frames to differ; got %q == %q", a, b)
	}
	// Frame index must wrap.
	wrap := statusGlyph("thinking", len(spinnerRunes))
	if wrap != a {
		t.Fatalf("expected wrap-around at frame=len; got %q vs %q", wrap, a)
	}
}

// TestStatusGlyph_OtherStatuses returns the documented static glyphs.
func TestStatusGlyph_OtherStatuses(t *testing.T) {
	cases := map[string]string{
		"idle":          "○",
		"awaiting_user": "⚡",
		"dead":          "✕",
		"unknown":       "·",
	}
	for status, want := range cases {
		got := statusGlyph(SessionStatus(status), 0)
		if got != want {
			t.Fatalf("statusGlyph(%q): want %q got %q", status, want, got)
		}
	}
}

// TestRenderList_ShowsSpinnerForThinkingSession checks the spinner
// rune actually lands in the rendered rail string for a thinking
// session at frame 0.
func TestRenderList_ShowsSpinnerForThinkingSession(t *testing.T) {
	rows := []RailRow{
		{Kind: RailRowSession, Session: Session{ID: "s1", Name: "alpha", Color: "#fff", Status: "thinking"}},
	}
	out := renderList(rows, 0, "s1", map[string]bool{}, "", 40, 10, 0, true)
	if !strings.Contains(out, string(spinnerRunes[0])) {
		t.Fatalf("expected spinner frame %q in rail output, got: %q", string(spinnerRunes[0]), out)
	}
}

// TestListMsg_RestartsSpinnerWhenSessionFlipsToThinking ensures that a
// listMsg arriving with a thinking session re-arms the spinner tick if
// it had previously stopped (no thinking sessions anymore → stopped →
// status flip back → tick resumes).
func TestListMsg_RestartsSpinnerWhenSessionFlipsToThinking(t *testing.T) {
	m := Model{
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		spinnerRunning: false, // tick had stopped because nothing was thinking
	}
	tm, cmd := m.Update(listMsg([]Session{{ID: "s1", Status: "thinking"}}))
	if cmd == nil {
		t.Fatalf("expected non-nil cmd batch from listMsg with a thinking session")
	}
	mm := tm.(Model)
	if !mm.spinnerRunning {
		t.Fatalf("listMsg should set spinnerRunning=true when a session is thinking and the tick was stopped")
	}
}
