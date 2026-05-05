// Package model — claude_history.go: state, RPC fetch, filter,
// reducer, and view for ModeClaudeHistory (the cross-project
// history browser, Phase 8d). Extracted from model.go so the
// state-machine for "all claude sessions ever" lives in one file.
//
// The mode enum value (ModeClaudeHistory) and the Model field
// (m.claudeHist) stay in model.go; everything modal-specific lives
// here.
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// claudeHistoryState backs ModeClaudeHistory: the entries fetched
// from the daemon plus the cursor and an optional substring filter.
type claudeHistoryState struct {
	entries []ClaudeHistoryEntry
	cursor  int
	query   string
	loaded  bool
	err     error
}

// ClaudeHistoryEntry mirrors the daemon-side ClaudeJsonlEntry shape.
// Kept in this package (not types.go) because the TUI is the only
// consumer of the cross-project history view.
type ClaudeHistoryEntry struct {
	ClaudeSessionID  string `json:"claude_session_id"`
	Cwd              string `json:"cwd"`
	FirstUserMessage string `json:"first_user_message"`
	MtimeMs          int64  `json:"mtime_ms"`
	Size             int64  `json:"size"`
}

// claudeHistoryLoadedMsg arrives when the cross-project scan
// returns. We populate the modal's entries (or set err on failure)
// and reset the cursor to 0 so the most-recent session is selected.
type claudeHistoryLoadedMsg struct {
	entries []ClaudeHistoryEntry
	err     error
}

// openClaudeHistoryModal switches to ModeClaudeHistory and fires the
// daemon RPC to populate the list. Until the result arrives, the
// modal renders a "loading…" line.
func (m *Model) openClaudeHistoryModal() tea.Cmd {
	m.mode = ModeClaudeHistory
	m.claudeHist = claudeHistoryState{}
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "list_all_claude_jsonls",
			map[string]any{"limit": 200})
		if err != nil {
			return claudeHistoryLoadedMsg{err: err}
		}
		var resp struct {
			Entries []ClaudeHistoryEntry `json:"entries"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return claudeHistoryLoadedMsg{err: err}
		}
		return claudeHistoryLoadedMsg{entries: resp.Entries}
	}
}

// claudeHistoryFiltered returns the entries matching the current
// substring query (case-insensitive over cwd OR first_user_message
// OR claude_session_id). Empty query = everything.
func (m Model) claudeHistoryFiltered() []ClaudeHistoryEntry {
	q := strings.ToLower(strings.TrimSpace(m.claudeHist.query))
	if q == "" {
		return m.claudeHist.entries
	}
	out := make([]ClaudeHistoryEntry, 0, len(m.claudeHist.entries))
	for _, e := range m.claudeHist.entries {
		if strings.Contains(strings.ToLower(e.Cwd), q) ||
			strings.Contains(strings.ToLower(e.FirstUserMessage), q) ||
			strings.Contains(strings.ToLower(e.ClaudeSessionID), q) {
			out = append(out, e)
		}
	}
	return out
}

// handleKeyClaudeHistory is the reducer for ModeClaudeHistory. Enter
// resumes the selected session via spawn_session with
// resume_claude_session_id; Esc cancels; ↑↓ navigate; type to filter.
func (m Model) handleKeyClaudeHistory(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		m.claudeHist = claudeHistoryState{}
		return m, nil
	case "up":
		if m.claudeHist.cursor > 0 {
			m.claudeHist.cursor--
		}
		return m, nil
	case "down":
		filtered := m.claudeHistoryFiltered()
		if m.claudeHist.cursor < len(filtered)-1 {
			m.claudeHist.cursor++
		}
		return m, nil
	case "enter":
		filtered := m.claudeHistoryFiltered()
		if m.claudeHist.cursor < 0 || m.claudeHist.cursor >= len(filtered) {
			return m, nil
		}
		entry := filtered[m.claudeHist.cursor]
		m.mode = ModeMain
		m.claudeHist = claudeHistoryState{}
		// Derive a fresh chubby session name from the cwd's basename
		// so the rail row reads naturally rather than as the raw uuid.
		name := _historyDerivedName(entry.Cwd, entry.ClaudeSessionID, m.sessions)
		c := m.client
		sid := entry.ClaudeSessionID
		cwd := entry.Cwd
		return m, func() tea.Msg {
			_, err := c.Call(context.Background(), "spawn_session",
				map[string]any{
					"name":                     name,
					"cwd":                      cwd,
					"tags":                     []string{},
					"resume_claude_session_id": sid,
				})
			if err != nil {
				return errMsg{err}
			}
			return spawnDoneMsg{}
		}
	case "backspace":
		if len(m.claudeHist.query) > 0 {
			m.claudeHist.query = m.claudeHist.query[:len(m.claudeHist.query)-1]
			m.claudeHist.cursor = 0
		}
		return m, nil
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		m.claudeHist.query += string(msg.Runes)
		m.claudeHist.cursor = 0
	}
	if msg.Type == tea.KeySpace {
		m.claudeHist.query += " "
		m.claudeHist.cursor = 0
	}
	return m, nil
}

// _historyDerivedName picks a human-friendly chubby session name for
// a resumed claude session. Strategy: basename of the cwd; if the
// name's already taken by a live session, append "-<short-sid>".
func _historyDerivedName(cwd, sid string, existing []Session) string {
	base := "resumed"
	if cwd != "" {
		// e.g. /Users/foo/myrepo → "myrepo"
		i := strings.LastIndex(cwd, "/")
		if i >= 0 && i+1 < len(cwd) {
			base = cwd[i+1:]
		} else if cwd != "" {
			base = cwd
		}
	}
	taken := make(map[string]bool, len(existing))
	for _, s := range existing {
		taken[s.Name] = true
	}
	if !taken[base] {
		return base
	}
	short := sid
	if len(short) > 8 {
		short = short[:8]
	}
	return base + "-" + short
}

// viewClaudeHistory renders the cross-project history modal.
func (m Model) viewClaudeHistory() string {
	w, h := m.width, m.height
	if w < 1 {
		w = 80
	}
	if h < 1 {
		h = 20
	}
	innerW := w - 8
	if innerW < 50 {
		innerW = 50
	}
	innerH := h - 8
	if innerH < 8 {
		innerH = 8
	}

	var b strings.Builder
	b.WriteString(views.Bold.Render("All claude sessions") + "\n")
	b.WriteString(views.Dim.Render(
		"  resume any historical session via claude --resume",
	) + "\n\n")

	if m.claudeHist.err != nil {
		b.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			Render("error: "+m.claudeHist.err.Error()) + "\n")
	} else if !m.claudeHist.loaded && len(m.claudeHist.entries) == 0 {
		b.WriteString(views.Dim.Render("loading…") + "\n")
	} else {
		queryLine := views.Cyan.Render("▸ ") + m.claudeHist.query
		if m.claudeHist.query == "" {
			queryLine += views.Dim.Render(" (type to filter)")
		}
		b.WriteString(queryLine + "\n\n")

		filtered := m.claudeHistoryFiltered()
		if len(filtered) == 0 {
			b.WriteString(views.Dim.Render("(no matches)") + "\n")
		}
		// Window the list around the cursor so a user with hundreds
		// of historical sessions can still navigate.
		const maxRows = 12
		shown := filtered
		offset := 0
		if len(shown) > maxRows {
			start := m.claudeHist.cursor - maxRows/2
			if start < 0 {
				start = 0
			}
			end := start + maxRows
			if end > len(shown) {
				end = len(shown)
				start = end - maxRows
			}
			shown = shown[start:end]
			offset = start
		}
		for i, e := range shown {
			absIdx := offset + i
			marker := "  "
			if absIdx == m.claudeHist.cursor {
				marker = "▸ "
			}
			cwdShort := _abbreviateHome(e.Cwd)
			when := _humanizeMtimeMs(e.MtimeMs)
			head := lipgloss.NewStyle().Bold(absIdx == m.claudeHist.cursor)
			row := marker + head.Render(cwdShort) + "  " +
				views.Dim.Render(when)
			b.WriteString(row + "\n")
			if e.FirstUserMessage != "" {
				b.WriteString("    " +
					views.DimItalic.Render(e.FirstUserMessage) + "\n")
			}
		}
		if len(filtered) > maxRows {
			b.WriteString(views.Dim.Render(
				fmt.Sprintf("  ... %d more", len(filtered)-maxRows),
			) + "\n")
		}
	}

	b.WriteString("\n" + views.Dim.Render(
		"↑↓ navigate · Enter resume · Esc cancel",
	))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(innerW).
		Padding(0, 1).
		Render(b.String())
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

// _humanizeMtimeMs renders a Unix-millis timestamp as "Nh ago" /
// "Nd ago" / etc. — same shape moltty uses in its history tab.
func _humanizeMtimeMs(ms int64) string {
	now := time.Now().UnixMilli()
	delta := now - ms
	if delta < 0 {
		return "just now"
	}
	const (
		minute = 60 * 1000
		hour   = 60 * minute
		day    = 24 * hour
	)
	switch {
	case delta < minute:
		return "just now"
	case delta < hour:
		return fmt.Sprintf("%dm ago", delta/minute)
	case delta < day:
		return fmt.Sprintf("%dh ago", delta/hour)
	default:
		return fmt.Sprintf("%dd ago", delta/day)
	}
}
