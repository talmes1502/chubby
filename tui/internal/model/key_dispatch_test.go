package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// TestPrintableInRail_ForwardsToPty: with the rail pane active, a
// printable letter must reach claude's PTY (via routeKeyToPty).
// Pre-fix the default branch dropped the key when activePane !=
// PaneConversation, so users staring at a focused live session got
// no response when they typed.
//
// We use an empty sessions list so routeKeyToPty short-circuits to
// nil — the assertion is that handleKeyMain DELEGATED to
// routeKeyToPty, not that the RPC went out (which would need a live
// daemon).
func TestPrintableInRail_ForwardsToPty(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{},
		focused:        -1,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	// 'h' isn't bound to any rail-nav case, so pre-fix it would have
	// been dropped silently. Post-fix it falls through to routeKeyToPty
	// (which returns nil here because there's no focused session).
	out, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if out.(Model).activePane != PaneRail {
		t.Fatalf("active pane should not change on a forwarded printable")
	}
	if cmd != nil {
		t.Fatalf("with no focused session routeKeyToPty returns nil; got non-nil cmd")
	}
}

// TestCtrlD_InRail_NotHijacked: Ctrl+D is claude's EOF/exit chord,
// so it must reach the PTY. Pre-fix it was bound to "rail half-page
// down" alongside PgDn — that hijack made claude unable to receive
// Ctrl+D when the rail happened to be active.
func TestCtrlD_InRail_NotHijacked(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{},
		focused:        -1,
		railCursor:     3,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlD})
	if out.(Model).railCursor != 3 {
		t.Fatalf("Ctrl+D in rail must NOT walk the rail cursor; got %d, want 3",
			out.(Model).railCursor)
	}
}

// TestCtrlU_InRail_NotHijacked: same shape as Ctrl+D — readline's
// "kill line" must reach claude.
func TestCtrlU_InRail_NotHijacked(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{},
		focused:        -1,
		railCursor:     3,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlU})
	if out.(Model).railCursor != 3 {
		t.Fatalf("Ctrl+U in rail must NOT walk the rail cursor; got %d, want 3",
			out.(Model).railCursor)
	}
}

// TestPgDn_StillWalksRail: PgDn (the *real* half-page binding,
// distinct from Ctrl+D which was unbundled) still walks the rail.
func TestPgDn_StillWalksRail(t *testing.T) {
	sessions := make([]Session, 10)
	for i := range sessions {
		sessions[i] = Session{ID: "s", Name: "n"}
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
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyPgDown})
	if out.(Model).railCursor == 0 {
		t.Fatalf("PgDn in rail should advance railCursor")
	}
}
