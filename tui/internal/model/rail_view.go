// Package model — rail_view.go: left rail rendering and the per-status
// glyph cycle. Pure-rendering code; cursor navigation lives in
// model.go (moveRailCursor, focusRailRow, etc.) so the input handlers
// can stay near each other.
package model

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/USER/chubby/tui/internal/views"
)

// spinnerFrames is the Braille-dot spinner cycle used to indicate that
// a session is "thinking" — the Claude wrapper has been injected to and
// hasn't replied yet. Bright yellow (color 11) so it pops next to dim
// idle dots in the rail.
const spinnerFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

var spinnerRunes = []rune(spinnerFrames)

var spinnerStyle = views.Warn

// statusGlyph returns the rail/banner glyph for a session's status.
// For "thinking", frame indexes into the spinner cycle so successive
// renders animate; everything else is a static glyph. The returned
// string is already styled (color/bold) — callers should not wrap it
// in extra Foreground styles for "thinking" or they'll fight the
// vivid-yellow accent that distinguishes a working session from idle.
func statusGlyph(status SessionStatus, frame int) string {
	switch status {
	case StatusThinking:
		return spinnerStyle.Render(string(spinnerRunes[frame%len(spinnerRunes)]))
	case StatusAwaitingUser:
		return "⚡"
	case StatusDead:
		return "✕"
	case StatusIdle:
		return "○"
	default:
		return "·"
	}
}

// activePaneBorderColor / inactivePaneBorderColor are the lipgloss
// color codes used for the focused vs unfocused border of the rail
// and conversation panes (D8). 12 is the bright-blue accent already
// used elsewhere for "active" highlights; 240 is the dim grey we use
// for chrome we don't want competing for attention.
const (
	activePaneBorderColor   = lipgloss.Color("12")
	inactivePaneBorderColor = lipgloss.Color("240")
)

// renderList draws the grouped left rail. rows is the flattened
// header+session list from BuildRailRows; cursor is the highlighted
// row; focusedID is the currently-focused session's ID (gets bolded
// even if it's not the cursor row); searchHeader (optional) is
// rendered just below the "Sessions" title so the user sees the active
// filter; active toggles the border color so the user sees which pane
// owns arrow / paging keys (D8).
func renderList(rows []RailRow, cursor int, focusedID string, collapsed map[string]bool, searchHeader string, w, h, spinnerFrame int, active bool, cmdView string) string {
	var b strings.Builder
	b.WriteString(views.Bold.Render(" Sessions") + "\n")
	if searchHeader != "" {
		b.WriteString(views.Accent.
			Render(" "+searchHeader) + "\n")
	}
	folderStyle := views.AccentBold
	separatorStyle := views.DimItalic
	// Cursor indicator is a leading 1-cell color stripe in the leftmost
	// column. `│` (U+2502, Box Drawings Light Vertical) is Narrow-width
	// in Unicode — guaranteed 1 cell on every terminal — so the stripe
	// never shifts the indent. A row-wide Background tint was tried and
	// rejected as visually too heavy ("looks like a black box"); the
	// stripe gives a clear "you are here" without painting a rectangle.
	stripeStyle := views.Cyan
	leftCol := func(active bool) string {
		if active {
			return stripeStyle.Render("│")
		}
		return " "
	}
	for i, r := range rows {
		switch r.Kind {
		case RailRowUnfiledSeparator:
			// Folder block ends, unfiled block begins. A dim italic
			// "unfiled" label (no parens, lowercase) reads as a hint
			// rather than a header that could be misread as a folder.
			b.WriteString("  " + separatorStyle.Render("unfiled") + "\n")
			continue
		case RailRowFolder:
			glyph := "📁"
			if collapsed[r.GroupName] {
				glyph = "📁▸"
			}
			b.WriteString(leftCol(i == cursor) + " " +
				folderStyle.Render(fmt.Sprintf("%s %s", glyph, r.GroupName)) +
				"\n")
		case RailRowSession:
			s := r.Session
			col := lipgloss.Color(s.Color)
			nameStyle := lipgloss.NewStyle().Foreground(col)
			if s.ID == focusedID {
				// Focus is conveyed by bold + the already-applied color;
				// no extra marker so the column never shifts.
				nameStyle = nameStyle.Bold(true)
			}
			glyph := statusGlyph(s.Status, spinnerFrame)
			b.WriteString(leftCol(i == cursor) + "   " +
				nameStyle.Render(s.Name) + " " + glyph + "\n")
		}
	}
	// Bottom-of-rail chubby command palette. cmdView is non-empty
	// when ModeRailCommand is active (or a stale error is being
	// shown). Rendered as the last line(s) of the rail body so it
	// sits flush above the bottom border.
	body := b.String()
	if cmdView != "" {
		body += "\n" + cmdView
	} else {
		// Always show a dim hint when the palette is dormant — the
		// gesture (':') isn't discoverable otherwise.
		body += "\n" + views.DimItalic.Render(" :  for chubby command")
	}
	borderColor := inactivePaneBorderColor
	if active {
		borderColor = activePaneBorderColor
	}
	return lipgloss.NewStyle().
		Width(w).Height(h).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Render(body)
}
