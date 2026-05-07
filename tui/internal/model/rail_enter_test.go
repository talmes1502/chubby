package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// TestEnter_RailRow_AlreadyFocused_SwitchesToConversation: rail-list
// "enter to enter the row" semantics — Enter on a session row (even
// the one that's already focused) navigates INTO the session's
// conversation pane. Pre-pivot Enter on an already-focused row
// forwarded a CR to claude as a one-tap Y/N confirmation shortcut,
// but users found it unintuitive: they expected "enter the session",
// not "submit a stray newline from the rail". They can still
// confirm Y/N prompts by pressing Enter once they've landed in the
// conversation pane.
func TestEnter_RailRow_AlreadyFocused_SwitchesToConversation(t *testing.T) {
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
	mm := out.(Model)
	if mm.focused != 0 {
		t.Fatalf("focused should stay at 0; got %d", mm.focused)
	}
	if mm.activePane != PaneConversation {
		t.Fatalf("Enter on session row should switch to conversation pane; got %v", mm.activePane)
	}
	if cmd != nil {
		t.Fatalf("Enter on rail session row should NOT forward to PTY; got non-nil cmd")
	}
}

// TestEnter_RailRow_DifferentSession_FocusesAndSwitchesPane: when
// the rail cursor is on a row that is NOT the focused session, Enter
// focuses that row AND switches to the conversation pane in one
// keystroke — same "enter to enter the session" rule.
func TestEnter_RailRow_DifferentSession_FocusesAndSwitchesPane(t *testing.T) {
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
	mm := out.(Model)
	if mm.focused != 1 {
		t.Fatalf("Enter on non-focused row should focus it; focused=%d, want 1", mm.focused)
	}
	if mm.activePane != PaneConversation {
		t.Fatalf("Enter on non-focused row should switch to conversation pane; got %v", mm.activePane)
	}
	if cmd != nil {
		t.Fatalf("Enter on non-focused row should NOT forward to PTY; got non-nil cmd")
	}
}
