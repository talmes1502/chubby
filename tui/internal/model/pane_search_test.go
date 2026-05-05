package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

func _paneSearchModel(snapshot []string) Model {
	return Model{
		mode:           ModePaneSearch,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
		paneSearch: paneSearchState{
			sessionID: "s1",
			snapshot:  snapshot,
		},
	}
}

func TestPaneSearch_RecomputeMatchesCaseInsensitiveSubstring(t *testing.T) {
	s := paneSearchState{snapshot: []string{
		"Hello WORLD",
		"unrelated line",
		"another world here",
	}}
	s.query = "world"
	s.recompute()
	if len(s.matches) != 2 {
		t.Fatalf("expected 2 matches; got %d (%+v)", len(s.matches), s.matches)
	}
	if s.matches[0].line != 1 || s.matches[1].line != 3 {
		t.Fatalf("matches at wrong line numbers: %+v", s.matches)
	}
}

func TestPaneSearch_EmptyQueryClearsMatches(t *testing.T) {
	s := paneSearchState{snapshot: []string{"hello"}}
	s.query = "hello"
	s.recompute()
	if len(s.matches) != 1 {
		t.Fatalf("baseline: expected 1 match")
	}
	s.query = ""
	s.recompute()
	if len(s.matches) != 0 {
		t.Fatalf("empty query must clear matches; got %d", len(s.matches))
	}
}

func TestPaneSearch_TypingExtendsQuery(t *testing.T) {
	m := _paneSearchModel([]string{"alpha", "beta"})
	out, _ := m.handleKeyPaneSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("al")})
	got := out.(Model).paneSearch.query
	if got != "al" {
		t.Fatalf("query after typing 'al' = %q, want 'al'", got)
	}
	if len(out.(Model).paneSearch.matches) != 1 {
		t.Fatalf("expected 1 match for 'al', got %d", len(out.(Model).paneSearch.matches))
	}
}

func TestPaneSearch_BackspaceShortensQuery(t *testing.T) {
	m := _paneSearchModel([]string{"alpha", "beta"})
	m.paneSearch.query = "alpha"
	m.paneSearch.recompute()
	out, _ := m.handleKeyPaneSearch(tea.KeyMsg{Type: tea.KeyBackspace})
	got := out.(Model).paneSearch.query
	if got != "alph" {
		t.Fatalf("query after backspace = %q, want 'alph'", got)
	}
}

func TestPaneSearch_DownClampsAtLastMatch(t *testing.T) {
	m := _paneSearchModel([]string{"alpha", "beta", "alpha2"})
	m.paneSearch.query = "alpha"
	m.paneSearch.recompute()
	if len(m.paneSearch.matches) != 2 {
		t.Fatalf("setup: expected 2 matches")
	}
	// Walk past the end — must clamp at len-1.
	for i := 0; i < 5; i++ {
		out, _ := m.handleKeyPaneSearch(tea.KeyMsg{Type: tea.KeyDown})
		m = out.(Model)
	}
	if m.paneSearch.cursor != 1 {
		t.Fatalf("cursor should clamp at 1 (len-1); got %d", m.paneSearch.cursor)
	}
}

func TestPaneSearch_UpClampsAtZero(t *testing.T) {
	m := _paneSearchModel([]string{"alpha"})
	out, _ := m.handleKeyPaneSearch(tea.KeyMsg{Type: tea.KeyUp})
	if out.(Model).paneSearch.cursor != 0 {
		t.Fatalf("cursor must not go negative; got %d", out.(Model).paneSearch.cursor)
	}
}

func TestPaneSearch_EscReturnsToMain(t *testing.T) {
	m := _paneSearchModel([]string{"alpha"})
	out, _ := m.handleKeyPaneSearch(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := out.(Model)
	if m2.mode != ModeMain {
		t.Fatalf("Esc should return to ModeMain; got %v", m2.mode)
	}
	if len(m2.paneSearch.snapshot) != 0 {
		t.Fatalf("Esc should clear paneSearch state")
	}
}
