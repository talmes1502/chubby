// Package model — slash_popup.go: Claude-style autocomplete popup that
// renders below the compose bar while the user is typing a slash
// command. Up/Down moves the highlight, Enter accepts, Esc closes.
//
// State lives on Model (slashPopupCursor + slashPopupCmds); this file
// provides the recompute helper, the visibility predicate, and
// in-popup key handling. Rendering is in views/slash.go.
package model

import (
	"strings"

	"github.com/USER/chubby/tui/internal/views"
)

// updateSlashPopup recomputes the matching commands based on the
// current compose value. Returns whether the popup should be visible.
//
// Visibility rules:
//   - compose must start with "/"
//   - no space typed yet (we're still naming the command, not args)
//   - at least one matching command in the catalog
//
// Mutates m.slashPopupCmds and clamps m.slashPopupCursor.
func (m *Model) updateSlashPopup() bool {
	val := m.compose.Value()
	trimmed := strings.TrimSpace(val)
	if !strings.HasPrefix(trimmed, "/") {
		m.slashPopupCmds = nil
		m.slashPopupCursor = 0
		return false
	}
	// Already entered an arg? Don't show command popup; the existing
	// arg-ghost / Tab-cycle handles arg completion.
	if strings.Contains(trimmed, " ") {
		m.slashPopupCmds = nil
		m.slashPopupCursor = 0
		return false
	}
	prefix := strings.TrimPrefix(trimmed, "/")
	matches := views.MatchSlashCommands(prefix)
	m.slashPopupCmds = matches
	if m.slashPopupCursor >= len(matches) {
		m.slashPopupCursor = 0
	}
	return len(matches) > 0
}

// slashPopupVisible reports whether the slash-command autocomplete
// popup should be drawn below the compose bar.
func (m Model) slashPopupVisible() bool {
	return len(m.slashPopupCmds) > 0
}

// acceptSlashPopup writes the highlighted match into the compose bar
// (with a trailing space so the user can type/Tab the arg) and closes
// the popup. Caller must check slashPopupVisible() first.
func (m *Model) acceptSlashPopup() {
	cmd := m.slashPopupCmds[m.slashPopupCursor]
	v := "/" + cmd.Name + " "
	m.compose.SetValue(v)
	m.compose.SetCursor(len(v))
	m.slashPopupCmds = nil
	m.slashPopupCursor = 0
}
