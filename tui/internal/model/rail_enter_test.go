package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// TestEnter_RailRow_AlreadyFocused_ForwardsToPty: when the rail
// cursor is on the row that's ALREADY the focused session, Enter must
// forward to claude (so the user can submit prompts without an extra
// Tab into PaneConversation). Pre-fix Enter re-set focused = same idx
// and consumed the keystroke — claude never saw the Enter, leading to
// "I typed but Enter does nothing" reports.
//
// We assert via the same trick as ctrl_c_test.go: with no sessions
// the routeKeyToPty cmd short-circuits to nil (and definitely isn't
// tea.Quit), proving the dispatch went there rather than the
// rail-focus branch.
func TestEnter_RailRow_AlreadyFocused_ForwardsToPty(t *testing.T) {
	sessions := []Session{{ID: "s1", Name: "tal"}}
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       sessions,
		focused:        0,
		railCursor:     0, // cursor on s1 row (the already-focused one)
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEnter})
	// Focused stayed at 0 (no row change); routing went to PTY.
	if out.(Model).focused != 0 {
		t.Fatalf("focused should stay at 0; got %d", out.(Model).focused)
	}
	// routeKeyToPty with focused session and no client returns a
	// thunk that *would* call the RPC. We can't safely invoke it
	// (panics on nil client), so we just check the cmd is non-nil.
	if cmd == nil {
		t.Fatalf("Enter on already-focused row should forward to PTY (non-nil cmd)")
	}
}

// TestEnter_RailRow_DifferentSession_FocusesIt: when the rail cursor
// is on a row that is NOT the focused session, Enter focuses that
// row (no PTY forward). This is the behavior users rely on to switch
// between sessions with the keyboard.
func TestEnter_RailRow_DifferentSession_FocusesIt(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "alpha"},
		{ID: "s2", Name: "beta"},
	}
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       sessions,
		focused:        0,
		railCursor:     1, // cursor on beta, focus is on alpha
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEnter})
	if out.(Model).focused != 1 {
		t.Fatalf("Enter on non-focused row should focus it; focused=%d, want 1",
			out.(Model).focused)
	}
	if cmd != nil {
		t.Fatalf("Enter on non-focused row should NOT forward to PTY; got non-nil cmd")
	}
}
