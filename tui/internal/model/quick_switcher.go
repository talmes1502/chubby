// Package model — quick_switcher.go: state, match algorithm,
// reducer, and view for ModeQuickSwitcher (Phase 7's Cmd-P-style
// fuzzy session picker). Extracted from model.go.
//
// The mode enum value (ModeQuickSwitcher) and the Model field
// (m.quickSwitch) stay in model.go; everything modal-specific lives
// here.
package model

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// quickSwitcherState backs ModeQuickSwitcher: a single textinput
// for the query plus the cursor index into the filtered match set.
// Match algorithm: case-insensitive substring match against name OR
// cwd. Same shape as moltty's QuickSwitcher.tsx.
type quickSwitcherState struct {
	query  string
	cursor int
}

// quickSwitcherMatches returns the indices of m.sessions whose name
// or cwd contains ``query`` (case-insensitive). Order preserved from
// m.sessions. Empty query matches everything so the modal opens with
// the full session list.
func (m Model) quickSwitcherMatches(query string) []int {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]int, 0, len(m.sessions))
	for i, s := range m.sessions {
		if q == "" {
			out = append(out, i)
			continue
		}
		hayName := strings.ToLower(s.Name)
		hayCwd := strings.ToLower(s.Cwd)
		if strings.Contains(hayName, q) || strings.Contains(hayCwd, q) {
			out = append(out, i)
		}
	}
	return out
}

// handleKeyQuickSwitcher is the reducer for ModeQuickSwitcher. Up/
// Down move the cursor through the filtered match list; Enter
// focuses the selected session and returns to ModeMain; Esc cancels;
// printable chars / backspace edit the query.
func (m Model) handleKeyQuickSwitcher(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		m.quickSwitch = quickSwitcherState{}
		return m, nil
	case "up":
		if m.quickSwitch.cursor > 0 {
			m.quickSwitch.cursor--
		}
		return m, nil
	case "down":
		matches := m.quickSwitcherMatches(m.quickSwitch.query)
		if m.quickSwitch.cursor < len(matches)-1 {
			m.quickSwitch.cursor++
		}
		return m, nil
	case "enter":
		matches := m.quickSwitcherMatches(m.quickSwitch.query)
		if m.quickSwitch.cursor >= 0 && m.quickSwitch.cursor < len(matches) {
			m.focused = matches[m.quickSwitch.cursor]
		}
		m.mode = ModeMain
		m.quickSwitch = quickSwitcherState{}
		return m, nil
	case "backspace":
		if len(m.quickSwitch.query) > 0 {
			m.quickSwitch.query = m.quickSwitch.query[:len(m.quickSwitch.query)-1]
			m.quickSwitch.cursor = 0
		}
		return m, nil
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		m.quickSwitch.query += string(msg.Runes)
		// Reset cursor on each edit so the user always lands on the
		// top match without surprise — same UX as moltty.
		m.quickSwitch.cursor = 0
	}
	if msg.Type == tea.KeySpace {
		m.quickSwitch.query += " "
		m.quickSwitch.cursor = 0
	}
	return m, nil
}

// viewQuickSwitcher renders the centered fuzzy session picker.
func (m Model) viewQuickSwitcher() string {
	w := m.width / 2
	if w < 50 {
		w = 50
	}
	if w > 80 {
		w = 80
	}
	matches := m.quickSwitcherMatches(m.quickSwitch.query)

	var b strings.Builder
	b.WriteString(views.Bold.Render("Switch to session") + "\n\n")
	queryLine := views.Cyan.Render("▸ ") + m.quickSwitch.query
	if m.quickSwitch.query == "" {
		queryLine += views.Dim.Render(" (type to filter)")
	}
	b.WriteString(queryLine + "\n\n")

	if len(matches) == 0 {
		b.WriteString(views.Dim.Render("(no matches)") + "\n")
	}
	const maxRows = 12
	shown := matches
	if len(shown) > maxRows {
		// Keep the cursor in view: window the list around the cursor.
		start := m.quickSwitch.cursor - maxRows/2
		if start < 0 {
			start = 0
		}
		end := start + maxRows
		if end > len(shown) {
			end = len(shown)
			start = end - maxRows
		}
		shown = shown[start:end]
	}
	for i, idx := range shown {
		s := m.sessions[idx]
		marker := "  "
		// Compute the index relative to the unwindowed match list so
		// the cursor lines up with the absolute selection.
		absIdx := i
		if len(matches) > maxRows {
			absIdx = m.quickSwitch.cursor - maxRows/2 + i
			if absIdx < 0 {
				absIdx = i
			}
		}
		if absIdx == m.quickSwitch.cursor {
			marker = "▸ "
		}
		nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(s.Color))
		if absIdx == m.quickSwitch.cursor {
			nameStyle = nameStyle.Bold(true)
		}
		row := marker + nameStyle.Render(s.Name)
		if s.Cwd != "" {
			row += "  " + views.Dim.Render(_abbreviateHome(s.Cwd))
		}
		b.WriteString(row + "\n")
		// Phase 8c: dim preview line beneath the row showing the
		// first prompt the user ever sent in this session — helps
		// disambiguate "temp" / "temp-2" / "temp-3" rows by their
		// opening question rather than just their numeric suffix.
		if s.FirstUserMessage != "" {
			indent := "    "
			if absIdx == m.quickSwitch.cursor {
				indent = "    "
			}
			b.WriteString(indent + views.DimItalic.Render(s.FirstUserMessage) + "\n")
		}
	}
	if len(matches) > maxRows {
		b.WriteString(views.Dim.Render(
			fmt.Sprintf("  ... %d more", len(matches)-maxRows),
		) + "\n")
	}
	b.WriteString("\n" + views.Dim.Render(
		"↑↓ navigate · Enter focus · Esc cancel",
	))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(w).
		Padding(0, 1).
		Render(b.String())
	wh, hh := m.width, m.height
	if wh < 1 {
		wh = w
	}
	if hh < 1 {
		hh = 10
	}
	return lipgloss.Place(wh, hh, lipgloss.Center, lipgloss.Center, box)
}
