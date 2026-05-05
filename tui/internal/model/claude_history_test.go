package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

func _historyModel(entries []ClaudeHistoryEntry) Model {
	return Model{
		mode:           ModeClaudeHistory,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
		claudeHist: claudeHistoryState{
			entries: entries,
			loaded:  true,
		},
	}
}

func TestClaudeHistoryFiltered_EmptyQueryReturnsAll(t *testing.T) {
	m := _historyModel([]ClaudeHistoryEntry{
		{ClaudeSessionID: "a", Cwd: "/x"},
		{ClaudeSessionID: "b", Cwd: "/y"},
	})
	got := m.claudeHistoryFiltered()
	if len(got) != 2 {
		t.Fatalf("empty query → all entries; got %d", len(got))
	}
}

func TestClaudeHistoryFiltered_MatchesCwd(t *testing.T) {
	m := _historyModel([]ClaudeHistoryEntry{
		{ClaudeSessionID: "a", Cwd: "/Users/foo/myrepo"},
		{ClaudeSessionID: "b", Cwd: "/Users/foo/other"},
	})
	m.claudeHist.query = "myrepo"
	got := m.claudeHistoryFiltered()
	if len(got) != 1 || got[0].ClaudeSessionID != "a" {
		t.Fatalf("expected single myrepo match; got %+v", got)
	}
}

func TestClaudeHistoryFiltered_MatchesFirstUserMessage(t *testing.T) {
	m := _historyModel([]ClaudeHistoryEntry{
		{ClaudeSessionID: "a", Cwd: "/x", FirstUserMessage: "explain ssm"},
		{ClaudeSessionID: "b", Cwd: "/y", FirstUserMessage: "fix the bug"},
	})
	m.claudeHist.query = "ssm"
	got := m.claudeHistoryFiltered()
	if len(got) != 1 || got[0].ClaudeSessionID != "a" {
		t.Fatalf("first-message-substring should match a; got %+v", got)
	}
}

func TestClaudeHistory_EscapeCancels(t *testing.T) {
	m := _historyModel([]ClaudeHistoryEntry{{ClaudeSessionID: "a", Cwd: "/x"}})
	out, _ := m.handleKeyClaudeHistory(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := out.(Model)
	if m2.mode != ModeMain {
		t.Fatalf("Esc should return to ModeMain")
	}
	if len(m2.claudeHist.entries) != 0 {
		t.Fatalf("Esc should clear state")
	}
}

func TestClaudeHistory_DownNavigates(t *testing.T) {
	m := _historyModel([]ClaudeHistoryEntry{
		{ClaudeSessionID: "a", Cwd: "/x"},
		{ClaudeSessionID: "b", Cwd: "/y"},
	})
	out, _ := m.handleKeyClaudeHistory(tea.KeyMsg{Type: tea.KeyDown})
	if out.(Model).claudeHist.cursor != 1 {
		t.Fatalf("Down → cursor=1; got %d", out.(Model).claudeHist.cursor)
	}
}

func TestClaudeHistory_UpClampsAtZero(t *testing.T) {
	m := _historyModel([]ClaudeHistoryEntry{{ClaudeSessionID: "a", Cwd: "/x"}})
	out, _ := m.handleKeyClaudeHistory(tea.KeyMsg{Type: tea.KeyUp})
	if out.(Model).claudeHist.cursor != 0 {
		t.Fatalf("Up at 0 should stay at 0; got %d", out.(Model).claudeHist.cursor)
	}
}

func TestHistoryDerivedName_UsesBasename(t *testing.T) {
	got := _historyDerivedName("/Users/foo/myrepo", "abc-1234-5678", nil)
	if got != "myrepo" {
		t.Fatalf("name should be cwd basename; got %q", got)
	}
}

// When the basename's already a live session, append a short piece
// of the claude session id so the new chubby session has a unique
// name.
func TestHistoryDerivedName_AvoidsCollision(t *testing.T) {
	got := _historyDerivedName(
		"/Users/foo/myrepo",
		"abcdef0123456789",
		[]Session{{ID: "x", Name: "myrepo"}},
	)
	if got == "myrepo" {
		t.Fatalf("should disambiguate when myrepo is taken; got %q", got)
	}
}

func TestHumanizeMtime_Recent(t *testing.T) {
	now := time.Now().UnixMilli()
	if got := _humanizeMtimeMs(now); got != "just now" {
		t.Fatalf("now → 'just now'; got %q", got)
	}
}

func TestHumanizeMtime_Hours(t *testing.T) {
	twoH := time.Now().UnixMilli() - 2*60*60*1000
	if got := _humanizeMtimeMs(twoH); got != "2h ago" {
		t.Fatalf("2h ago expected; got %q", got)
	}
}
