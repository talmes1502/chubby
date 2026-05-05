// Package model — pane_search.go: in-pane scrollback search
// (ModePaneSearch). Modal over the focused session's PTY
// scrollback + visible screen. Type to filter substring,
// ↑↓ to walk matches, Enter copies the line to the clipboard,
// Esc cancels.
//
// Design: snapshot the pane's plain text once on open and again
// on each query change. Don't re-snapshot per arrow press —
// scrollback content is large and most user inputs are arrows.
//
// The snapshot is per-session (keyed by the focused session id at
// open time). Switching sessions cancels the modal — easier
// than juggling stale snapshots and matches your mental model.
package model

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// paneSearchMatch is one hit: the (1-based) line number in the
// snapshot, the full line text, and the byte offset of the match
// within that line. The snapshot is all-text scrollback + visible
// screen, oldest first.
type paneSearchMatch struct {
	line   int
	text   string
	offset int
}

// paneSearchState backs ModePaneSearch.
type paneSearchState struct {
	sessionID string
	snapshot  []string
	query     string
	matches   []paneSearchMatch
	cursor    int
	notice    string
}

// openPaneSearchModal flips to ModePaneSearch with a fresh snapshot
// of the focused session's pane. Returns nil cmd when there's nothing
// to search (no focused session, or the pane is in alt-screen mode
// and reports no scrollback).
func (m *Model) openPaneSearchModal() tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	pane := m.pty[s.ID]
	if pane == nil {
		return nil
	}
	lines := pane.PlainTextLines()
	if len(lines) == 0 {
		// Alt-screen or empty pane — nothing to search. We could
		// still open the modal with an empty result, but that's a
		// dead end for the user; better to no-op silently.
		return nil
	}
	m.mode = ModePaneSearch
	m.paneSearch = paneSearchState{
		sessionID: s.ID,
		snapshot:  lines,
	}
	return nil
}

// handleKeyPaneSearch is the reducer for ModePaneSearch.
func (m Model) handleKeyPaneSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		m.paneSearch = paneSearchState{}
		return m, nil
	case "up":
		if m.paneSearch.cursor > 0 {
			m.paneSearch.cursor--
		}
		return m, nil
	case "down":
		if m.paneSearch.cursor < len(m.paneSearch.matches)-1 {
			m.paneSearch.cursor++
		}
		return m, nil
	case "enter":
		// Copy the focused match's line to the clipboard so the user
		// can paste it elsewhere. This is the most-asked next-step
		// after finding a match (paste a stack trace into a bug
		// report, paste a path into the editor, etc.). Esc to close.
		if len(m.paneSearch.matches) == 0 {
			return m, nil
		}
		c := m.paneSearch.cursor
		if c < 0 || c >= len(m.paneSearch.matches) {
			return m, nil
		}
		text := m.paneSearch.matches[c].text
		if err := clipboard.WriteAll(text); err != nil {
			m.paneSearch.notice = "clipboard write failed: " + err.Error()
		} else {
			m.paneSearch.notice = "copied"
		}
		return m, nil
	case "backspace":
		if len(m.paneSearch.query) > 0 {
			m.paneSearch.query = m.paneSearch.query[:len(m.paneSearch.query)-1]
			m.paneSearch.recompute()
		}
		return m, nil
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		m.paneSearch.query += string(msg.Runes)
		m.paneSearch.recompute()
	}
	if msg.Type == tea.KeySpace {
		m.paneSearch.query += " "
		m.paneSearch.recompute()
	}
	return m, nil
}

// recompute re-runs the substring filter against the cached snapshot.
// Case-insensitive, plain substring (no regex — keeps the contract
// predictable; if the user wants regex they'll grep the JSONL).
// Resets cursor to 0 on any query change so the modal always lands
// on the top match.
func (s *paneSearchState) recompute() {
	q := strings.ToLower(strings.TrimSpace(s.query))
	s.matches = s.matches[:0]
	s.cursor = 0
	s.notice = ""
	if q == "" {
		return
	}
	for i, line := range s.snapshot {
		// Skip the line if it's just the prompt glyph or a blank row;
		// otherwise we get a flood of "▸ " hits the user doesn't care
		// about.
		idx := strings.Index(strings.ToLower(line), q)
		if idx < 0 {
			continue
		}
		s.matches = append(s.matches, paneSearchMatch{
			line:   i + 1,
			text:   line,
			offset: idx,
		})
	}
}

// viewPaneSearch renders the search modal: input + match count +
// windowed list of matches with the active match highlighted.
func (m Model) viewPaneSearch() string {
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

	var b strings.Builder
	b.WriteString(views.Bold.Render("Find in pane") + "\n")
	if name := m.focusedSessionName(); name != "" {
		b.WriteString(views.Dim.Render("  searching "+name) + "\n")
	}
	b.WriteString("\n")

	queryLine := views.Cyan.Render("▸ ") + m.paneSearch.query
	if m.paneSearch.query == "" {
		queryLine += views.Dim.Render(" (type to filter)")
	}
	b.WriteString(queryLine + "\n")
	if len(m.paneSearch.matches) > 0 || m.paneSearch.query != "" {
		count := fmt.Sprintf("%d match", len(m.paneSearch.matches))
		if len(m.paneSearch.matches) != 1 {
			count += "es"
		}
		b.WriteString(views.Dim.Render("  "+count) + "\n")
	}
	b.WriteString("\n")

	const maxRows = 12
	matches := m.paneSearch.matches
	shown := matches
	offset := 0
	if len(shown) > maxRows {
		start := m.paneSearch.cursor - maxRows/2
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
	for i, mtch := range shown {
		absIdx := offset + i
		marker := "  "
		if absIdx == m.paneSearch.cursor {
			marker = "▸ "
		}
		lineNum := views.Dim.Render(fmt.Sprintf("L%d ", mtch.line))
		body := mtch.text
		// Trim very long lines so the modal doesn't blow up width.
		const maxLineW = 100
		if len(body) > maxLineW {
			body = body[:maxLineW] + "…"
		}
		head := lipgloss.NewStyle().Bold(absIdx == m.paneSearch.cursor)
		b.WriteString(marker + lineNum + head.Render(body) + "\n")
	}
	if len(matches) > maxRows {
		b.WriteString(views.Dim.Render(
			fmt.Sprintf("  ... %d more", len(matches)-maxRows),
		) + "\n")
	}

	if m.paneSearch.notice != "" {
		b.WriteString("\n" + views.Cyan.Render(m.paneSearch.notice) + "\n")
	}
	b.WriteString("\n" + views.Dim.Render(
		"↑↓ navigate · Enter copy line · Esc cancel",
	))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(innerW).
		Padding(0, 1).
		Render(b.String())
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

// focusedSessionName is a small helper used by the search modal
// header. Returns "" when no session is focused (which shouldn't
// happen since openPaneSearchModal refuses that case).
func (m Model) focusedSessionName() string {
	s := m.focusedSession()
	if s == nil {
		return ""
	}
	return s.Name
}
