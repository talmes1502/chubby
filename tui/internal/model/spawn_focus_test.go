package model

import (
	"testing"
	"time"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// spawnDoneMsg carries the new session's id; the listMsg handler
// resolves it and pins focus on the new row. Without this, the user
// has to scan the rail for the row they just created, which is a
// papercut every time you Ctrl+N.
func TestSpawnDone_FocusesNewSessionOnNextList(t *testing.T) {
	m := Model{
		mode:              ModeMain,
		compose:           views.NewCompose(),
		groupCollapsed:    map[string]bool{},
		scrollOffset:      map[string]int{},
		newSinceScroll:    map[string]int{},
		thinkingStartedAt: map[string]time.Time{},
		conversation:      map[string][]Turn{},
		historyLoaded:     map[string]bool{},
		sessions: []Session{
			{ID: "old1", Name: "alpha"},
			{ID: "old2", Name: "beta"},
		},
		focused: 0,
	}

	// A spawnDoneMsg arrives with the new session's id.
	out, _ := m.Update(spawnDoneMsg{id: "new3"})
	mm := out.(Model)
	if mm.pendingFocusID != "new3" {
		t.Fatalf("spawnDoneMsg should arm pendingFocusID; got %q", mm.pendingFocusID)
	}

	// Now the daemon's refreshSessions returns a list that includes
	// the new session. listMsg should consume the pendingFocusID and
	// snap focus to it.
	listed := listMsg([]Session{
		{ID: "old1", Name: "alpha"},
		{ID: "old2", Name: "beta"},
		{ID: "new3", Name: "gamma"},
	})
	out, _ = mm.Update(listed)
	mm = out.(Model)
	if mm.focused != 2 {
		t.Fatalf("focused should be 2 (the new session); got %d", mm.focused)
	}
	if mm.pendingFocusID != "" {
		t.Fatalf("pendingFocusID should clear after consumption; got %q", mm.pendingFocusID)
	}
	if mm.activePane != PaneConversation {
		t.Fatalf("spawning should also flip activePane to Conversation so the user lands in claude's input")
	}
}
