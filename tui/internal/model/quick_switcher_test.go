package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

func _switcherModel(sessions []Session) Model {
	return Model{
		mode:           ModeQuickSwitcher,
		compose:        views.NewCompose(),
		sessions:       sessions,
		quickSwitch:    quickSwitcherState{},
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
}

func TestQuickSwitcherMatches_EmptyQueryReturnsAll(t *testing.T) {
	m := _switcherModel([]Session{
		{ID: "a", Name: "alpha", Cwd: "/x"},
		{ID: "b", Name: "beta", Cwd: "/y"},
	})
	got := m.quickSwitcherMatches("")
	if len(got) != 2 {
		t.Fatalf("empty query → all sessions; got %v", got)
	}
}

func TestQuickSwitcherMatches_SubstringByName(t *testing.T) {
	m := _switcherModel([]Session{
		{ID: "a", Name: "frontend", Cwd: "/x"},
		{ID: "b", Name: "backend", Cwd: "/y"},
		{ID: "c", Name: "infra", Cwd: "/z"},
	})
	got := m.quickSwitcherMatches("end")
	if len(got) != 2 {
		t.Fatalf("'end' should match 'frontend' + 'backend'; got %v", got)
	}
}

func TestQuickSwitcherMatches_SubstringByCwd(t *testing.T) {
	m := _switcherModel([]Session{
		{ID: "a", Name: "web", Cwd: "/Users/foo/myrepo"},
		{ID: "b", Name: "api", Cwd: "/Users/foo/other"},
	})
	got := m.quickSwitcherMatches("myrepo")
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("cwd-substring match should pick web; got %v", got)
	}
}

func TestQuickSwitcherMatches_CaseInsensitive(t *testing.T) {
	m := _switcherModel([]Session{
		{ID: "a", Name: "Frontend", Cwd: "/x"},
	})
	got := m.quickSwitcherMatches("FRONT")
	if len(got) != 1 {
		t.Fatalf("matcher should be case-insensitive; got %v", got)
	}
}

func TestQuickSwitcher_EnterFocusesSelected(t *testing.T) {
	m := _switcherModel([]Session{
		{ID: "a", Name: "web"},
		{ID: "b", Name: "api"},
		{ID: "c", Name: "infra"},
	})
	// Sessions: web=0, api=1, infra=2. Query "a" matches api + infra
	// (substring "a"). First match (cursor=0) is api → idx 1. Down
	// once → cursor=1 → infra → idx 2.
	for _, r := range "a" {
		out, _ := m.handleKeyQuickSwitcher(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{r},
		})
		m = out.(Model)
	}
	// Enter on the FIRST match (api, idx 1).
	out, _ := m.handleKeyQuickSwitcher(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if m.mode != ModeMain {
		t.Fatalf("Enter should return to ModeMain; got %v", m.mode)
	}
	if m.focused != 1 {
		t.Fatalf("Enter on first match should focus api (idx 1); got %d", m.focused)
	}

	// Re-open and walk to the second match.
	m = _switcherModel([]Session{
		{ID: "a", Name: "web"},
		{ID: "b", Name: "api"},
		{ID: "c", Name: "infra"},
	})
	for _, r := range "a" {
		out, _ := m.handleKeyQuickSwitcher(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{r},
		})
		m = out.(Model)
	}
	out, _ = m.handleKeyQuickSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	out, _ = m.handleKeyQuickSwitcher(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if m.focused != 2 {
		t.Fatalf("Down then Enter should focus second match infra (idx 2); got %d", m.focused)
	}
}

func TestQuickSwitcher_EscapeCancels(t *testing.T) {
	m := _switcherModel([]Session{
		{ID: "a", Name: "web"},
		{ID: "b", Name: "api"},
	})
	m.focused = 0
	for _, r := range "api" {
		out, _ := m.handleKeyQuickSwitcher(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{r},
		})
		m = out.(Model)
	}
	out, _ := m.handleKeyQuickSwitcher(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(Model)
	if m.mode != ModeMain {
		t.Fatalf("Esc should return to ModeMain")
	}
	// Focus must NOT have moved — Esc is a cancel, not a select.
	if m.focused != 0 {
		t.Fatalf("Esc should not change focus; got %d", m.focused)
	}
	// Query state cleared so next open starts fresh.
	if m.quickSwitch.query != "" {
		t.Fatalf("Esc should clear query state; got %q", m.quickSwitch.query)
	}
}

func TestQuickSwitcher_BackspaceTrimsQuery(t *testing.T) {
	m := _switcherModel([]Session{{ID: "a", Name: "web"}})
	for _, r := range "abc" {
		out, _ := m.handleKeyQuickSwitcher(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{r},
		})
		m = out.(Model)
	}
	out, _ := m.handleKeyQuickSwitcher(tea.KeyMsg{Type: tea.KeyBackspace})
	m = out.(Model)
	if m.quickSwitch.query != "ab" {
		t.Fatalf("backspace should trim 1 char; got %q", m.quickSwitch.query)
	}
}

// TestCtrlP_OpensSwitcherWhenFocusedIsNotDead: the smart Ctrl+P
// overload — DEAD focus → respawn (existing behavior); else open
// the switcher.
func TestCtrlP_OpensSwitcherWhenFocusedIsNotDead(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		sessions:       []Session{{ID: "a", Name: "web", Status: StatusIdle}},
		focused:        0,
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
		activePane:     PaneRail,
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlP})
	m2 := out.(Model)
	if m2.mode != ModeQuickSwitcher {
		t.Fatalf("Ctrl+P on non-DEAD session should open ModeQuickSwitcher; got %v", m2.mode)
	}
}

// Existing TestCtrlP_OnlyFiresOnDeadSession in spawn_group_test.go
// covered the old behavior where Ctrl+P was a strict no-op for
// non-dead sessions. With the smart overload it now opens the
// switcher instead — that test's expectation is already obsolete
// for non-dead sessions. The DEAD case still fires respawn,
// preserved by the conditional in handleKeyMain.
