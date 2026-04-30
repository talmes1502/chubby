// Package views — search.go: helpers for the ModeSearch session-rail filter.
package views

import "github.com/charmbracelet/bubbles/textinput"

// NewSearchQuery returns a focused textinput suitable for filtering
// the session list by name (case-insensitive substring).
func NewSearchQuery() textinput.Model {
	t := textinput.New()
	t.Placeholder = "type to filter sessions"
	t.Prompt = "/ "
	t.CharLimit = 0
	t.Focus()
	return t
}
