// Package views — rail_command.go: textinput for the bottom-of-rail
// chubby command palette. Activated by ':' from the rail; accepts
// the same /movetofolder, /color, /tag, /detach, /rename, /tag,
// /refresh-claude commands the old compose bar handled (and which
// were lost when the embedded-PTY pivot dropped the compose bar).
package views

import "github.com/charmbracelet/bubbles/textinput"

// NewRailCommand returns a focused textinput sized for the narrow
// rail column. The ':' prompt mirrors vim's command-line — the
// keystroke that opens the palette is the same character that
// prefixes the prompt, so the visual matches the gesture.
func NewRailCommand() textinput.Model {
	t := textinput.New()
	t.Placeholder = "/movetofolder, /color, /tag, /detach …"
	t.Prompt = ": "
	t.CharLimit = 0
	t.Focus()
	return t
}
