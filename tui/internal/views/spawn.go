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

// NewSpawnBranchInput returns an unfocused textinput for the optional
// branch name. When non-empty, the daemon resolves a chubby-managed
// git worktree at that branch and overrides the session's cwd with
// the worktree path. Empty value leaves the daemon to spawn into the
// raw cwd (the historical behavior).
func NewSpawnBranchInput(initial string) textinput.Model {
	t := textinput.New()
	t.Placeholder = "optional — git branch (creates worktree)"
	t.Prompt = "▸ "
	t.CharLimit = 0
	if initial != "" {
		t.SetValue(initial)
	}
	return t
}

// NewSpawnFolderInput returns an unfocused textinput for the optional
// folder name. After D10b the trimmed value, when non-empty, is used
// to assign the new session into a TUI-side folder (folders.json) —
// it is no longer passed to the daemon's spawn_session RPC as a tag.
// The folder is created if it doesn't already exist.
func NewSpawnFolderInput(initial string) textinput.Model {
	t := textinput.New()
	t.Placeholder = "optional — assign to folder (creates if missing)"
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
