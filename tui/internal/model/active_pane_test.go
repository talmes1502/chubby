package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chubby/tui/internal/views"
)

// TestTab_TogglesActivePane: with no compose text and no autocomplete
// to consume, Tab toggles activePane between PaneRail and
// PaneConversation.
func TestTab_TogglesActivePane(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api"}},
		focused:        0,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	if m.activePane != PaneRail {
		t.Fatalf("default activePane should be PaneRail, got %v", m.activePane)
	}
	tab := tea.KeyMsg{Type: tea.KeyTab}
	out, _ := m.handleKeyMain(tab)
	m = out.(Model)
	if m.activePane != PaneConversation {
		t.Fatalf("after first Tab expected PaneConversation, got %v", m.activePane)
	}
	out, _ = m.handleKeyMain(tab)
	m = out.(Model)
	if m.activePane != PaneRail {
		t.Fatalf("after second Tab expected PaneRail, got %v", m.activePane)
	}
}

// TestTab_DoesNotTogglePaneWhenSlashAutocompleteAvailable: when the
// compose bar starts with a slash command partial that has matches,
// Tab is consumed by the slash-command completion logic and must NOT
// flip the active pane.
func TestTab_DoesNotTogglePaneWhenSlashAutocompleteAvailable(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api"}},
		focused:        0,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	// Type a slash so trySlashComplete returns ok.
	m.compose.SetValue("/m")
	starting := m.activePane
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	if m.activePane != starting {
		t.Fatalf("Tab with slash autocomplete in flight should not toggle pane: starting=%v after=%v",
			starting, m.activePane)
	}
}

// TestArrowsInRailWalkRailCursor: with PaneRail active and compose
// empty, Up/Down move m.railCursor (which in turn updates m.focused
// when landing on a session row).
func TestArrowsInRailWalkRailCursor(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "alpha"},
		{ID: "s2", Name: "beta"},
		{ID: "s3", Name: "gamma"},
	}
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       sessions,
		focused:        0,
		railCursor:     0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	// Down moves cursor to row 1.
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	if m.railCursor != 1 {
		t.Fatalf("after Down expected railCursor=1, got %d", m.railCursor)
	}
	if m.focused != 1 {
		t.Fatalf("after Down expected focused=1, got %d", m.focused)
	}
	// Up wraps back to row 0.
	out, _ = m.handleKeyMain(tea.KeyMsg{Type: tea.KeyUp})
	m = out.(Model)
	if m.railCursor != 0 {
		t.Fatalf("after Up expected railCursor=0, got %d", m.railCursor)
	}
}

// TestArrowsInConversationScroll: with PaneConversation active and
// compose empty, Up/Down dispatch into the per-session scroll helpers
// rather than walking the rail.
func TestArrowsInConversationScroll(t *testing.T) {
	turns := make([]Turn, 50)
	for i := range turns {
		turns[i] = Turn{Role: "user", Text: "msg", Ts: int64(i)}
	}
	m := Model{
		mode:               ModeMain,
		compose:            views.NewCompose(),
		groupCollapsed:     map[string]bool{},
		sessions:           []Session{{ID: "s1", Name: "alpha", Color: "12"}},
		focused:            0,
		railCursor:         0,
		activePane:         PaneConversation,
		conversation:       map[string][]Turn{"s1": turns},
		scrollOffset:       map[string]int{},
		newSinceScroll:     map[string]int{},
		lastViewportInnerW: 60,
		lastViewportInnerH: 10,
	}
	// Up scrolls; railCursor stays put.
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyUp})
	m = out.(Model)
	if m.scrollOffset["s1"] != 1 {
		t.Fatalf("Up in conversation pane should scroll, scrollOffset=%d", m.scrollOffset["s1"])
	}
	if m.railCursor != 0 {
		t.Fatalf("Up in conversation pane must not walk rail cursor, got %d", m.railCursor)
	}
}

// TestEnd_ConversationJumpsToBottom: in PaneConversation, End pins
// the scrollOffset to 0.
func TestEnd_ConversationJumpsToBottom(t *testing.T) {
	m := Model{
		mode:               ModeMain,
		compose:            views.NewCompose(),
		groupCollapsed:     map[string]bool{},
		sessions:           []Session{{ID: "s1", Name: "alpha", Color: "12"}},
		focused:            0,
		activePane:         PaneConversation,
		conversation:       map[string][]Turn{"s1": {{Role: "user", Text: "x"}}},
		scrollOffset:       map[string]int{"s1": 5},
		newSinceScroll:     map[string]int{"s1": 3},
		lastViewportInnerW: 60,
		lastViewportInnerH: 10,
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEnd})
	m = out.(Model)
	if m.scrollOffset["s1"] != 0 {
		t.Fatalf("End in conversation pane should pin scrollOffset to 0, got %d", m.scrollOffset["s1"])
	}
	if m.newSinceScroll["s1"] != 0 {
		t.Fatalf("End should clear unread, got %d", m.newSinceScroll["s1"])
	}
}

// TestEnd_RailJumpsCursor: in PaneRail, End jumps the rail cursor to
// the last non-separator row.
func TestEnd_RailJumpsCursor(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "a"},
		{ID: "s2", Name: "b"},
		{ID: "s3", Name: "c"},
	}
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       sessions,
		focused:        0,
		railCursor:     0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEnd})
	m = out.(Model)
	rows := m.railRows()
	if len(rows) == 0 {
		t.Fatalf("rail rows shouldn't be empty in this test")
	}
	if m.railCursor != len(rows)-1 {
		t.Fatalf("End in rail pane should land on last row (%d), got %d", len(rows)-1, m.railCursor)
	}
}

// TestCtrlBackslash_StillCyclesSessionDirectly: the legacy
// "cycle focused session forward" power-user shortcut moved from
// Ctrl+Tab (which most terminals can't report distinctly from plain
// Tab) to Ctrl+\, alongside the existing Shift+Tab reverse cycle and
// the new pane-toggle Tab. Test name keeps the "CtrlTab" prefix so
// it lines up with the implementation plan even though the binding
// itself is Ctrl+\.
func TestCtrlTab_StillCyclesSessionDirectly(t *testing.T) {
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
		railCursor:     0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlBackslash})
	m = out.(Model)
	if m.focused != 1 {
		t.Fatalf("Ctrl+\\ should advance focused from 0 to 1, got %d", m.focused)
	}
	// activePane remains untouched.
	if m.activePane != PaneRail {
		t.Fatalf("Ctrl+\\ should not toggle pane, got %v", m.activePane)
	}
}
