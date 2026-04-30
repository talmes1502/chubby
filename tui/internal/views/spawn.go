// Package views — spawn.go: helpers for the ModeSpawn modal.
package views

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
)

// NewSpawnNameInput returns a focused textinput for the new session name.
func NewSpawnNameInput() textinput.Model {
	t := textinput.New()
	t.Placeholder = "session name (e.g. backend)"
	t.Prompt = "▸ "
	t.CharLimit = 0
	t.Focus()
	return t
}

// NewSpawnCwdInput returns an unfocused textinput for the cwd.
func NewSpawnCwdInput(initial string) textinput.Model {
	t := textinput.New()
	t.Placeholder = "directory (~ expands; tab to switch field)"
	t.Prompt = "▸ "
	t.CharLimit = 0
	if initial != "" {
		t.SetValue(initial)
	}
	return t
}

// ExpandHome expands a leading ~ or ~/ in a path. Returns the original
// string if the home directory can't be resolved or the path doesn't
// start with ~.
func ExpandHome(p string) string {
	if p == "" {
		return p
	}
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
