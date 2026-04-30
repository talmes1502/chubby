// Package views — spawn.go: helpers for the ModeSpawn modal.
package views

import "github.com/charmbracelet/bubbles/textinput"

// NewSpawnNameInput returns a focused textinput suitable for entering
// the new session name in the spawn modal.
func NewSpawnNameInput() textinput.Model {
	t := textinput.New()
	t.Placeholder = "session name"
	t.Prompt = "▸ "
	t.CharLimit = 0
	t.Focus()
	return t
}
