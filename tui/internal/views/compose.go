// Package views holds the per-mode UI helpers used by the chub-tui Model:
// compose bar, broadcast modal, grep palette, history panel.
package views

import (
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

// NewCompose returns a focused, single-line text input used as the
// bottom compose bar. Multiline insertion is achieved via a
// shift+enter handler in the Model that mutates Value directly.
func NewCompose() textinput.Model {
	t := textinput.New()
	t.Placeholder = "type a prompt, Enter to send, @name to retarget"
	t.Prompt = "▸ "
	t.CharLimit = 0
	t.Focus()
	return t
}

// RenderCompose draws the compose bar with a colored target prefix
// (the focused session's name + color). w is the total bar width
// including the rounded border. ghost (optional) is rendered as dim
// suffix after the textinput content — used to preview @-name
// autocompletion on the next Tab.
func RenderCompose(t textinput.Model, target, color, ghost string, w int) string {
	if w < 8 {
		w = 8
	}
	style := lipgloss.NewStyle().Width(w - 2).Border(lipgloss.RoundedBorder())
	prefix := lipgloss.NewStyle().
		Foreground(lipgloss.Color(color)).
		Bold(true).
		Render(target)
	body := t.View()
	if ghost != "" {
		body += lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Render(ghost)
	}
	return style.Render(prefix + " " + body)
}
