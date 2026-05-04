package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chubby/tui/internal/views"
)

// TestCtrlC_RailQuits: with the rail pane active, Ctrl+C returns
// tea.Quit so chubby exits cleanly. (In the conversation pane Ctrl+C
// is forwarded to the focused claude as its interrupt key — covered
// separately by routeKeyToPty.)
func TestCtrlC_RailQuits(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "a"}},
		focused:        0,
		railCursor:     0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	_, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("Ctrl+C in rail should return a non-nil cmd (tea.Quit)")
	}
	// tea.Quit is a thunk that returns tea.QuitMsg{} when invoked. The
	// standard way to assert "this cmd is tea.Quit" is to call it and
	// type-assert the message — comparing function pointers is
	// unreliable across Bubble Tea versions.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("Ctrl+C in rail should return tea.Quit, got %T", msg)
	}
}

// TestCtrlC_ConversationDoesNotQuit: with the conversation pane
// active, Ctrl+C must NOT quit chubby — it goes to claude via
// routeKeyToPty. We use an empty sessions list so routeKeyToPty
// short-circuits to nil (no focused session); this proves the
// dispatch went to routeKeyToPty rather than tea.Quit, without
// needing a live RPC client.
func TestCtrlC_ConversationDoesNotQuit(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{},
		focused:        -1,
		activePane:     PaneConversation,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	_, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		// The only path that returns a non-nil cmd from this state
		// would be tea.Quit — and that's exactly the bug we're
		// guarding against.
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Fatalf("Ctrl+C in conversation pane must NOT quit chubby")
		}
	}
}
