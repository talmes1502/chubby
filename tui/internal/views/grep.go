package views

import (
	"github.com/charmbracelet/bubbles/textinput"
)

// NewGrepQuery returns a focused textinput suitable for the grep
// palette query field. The "/" key is used to enter ModeGrep so the
// prompt rune is "/" to make that visually consistent.
func NewGrepQuery() textinput.Model {
	t := textinput.New()
	t.Placeholder = "search transcripts (FTS)"
	t.Prompt = "/ "
	t.CharLimit = 0
	t.Focus()
	return t
}
