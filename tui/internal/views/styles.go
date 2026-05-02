// Package views — styles.go: shared lipgloss styles used across the
// rail, viewport, banner, status bar, and modal panes. Hoisting them
// to package-level vars saves the per-frame allocation cost of
// reconstructing the same Style on every render and gives every
// caller one canonical reference for the chubby palette.
//
// Palette anchor:
//   - Color "240" — dim grey, chrome metadata (cwd, kind, status hints)
//   - Color "245" — slightly brighter grey, blockquote / dim italic
//   - Color "248" — list-item / lighter prose
//   - Color "250" — body code text
//   - Color  "12" — bright blue accent (folder headers, active borders)
//   - Color  "81" — soft cyan (#5fafff) — Claude-style links/headings
//   - Color  "11" — bright yellow (rail spinner, scrolled-up hint)
//
// Naming: Dim<Foo> for dim-grey variants, <Foo>Style for typed styles.
package views

import "github.com/charmbracelet/lipgloss"

// Dim is the standard "metadata / chrome" style: 240 grey, no bold.
// Used for cwd/kind hints, status-bar bottom-line copy, the activity-
// line's tokens-and-elapsed segment, and any other "this is context,
// not content" affordance.
var Dim = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// DimItalic adds italic to Dim — used for blockquotes and the rail's
// "unfiled" section hint.
var DimItalic = Dim.Italic(true)

// Accent is the bright-blue (color "12") chrome accent: folder header
// names, active pane border, search-header text. Reads as "this is
// chubby chrome" without competing with session-color content.
var Accent = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))

// AccentBold is Accent + bold — folder headers in the rail.
var AccentBold = Accent.Bold(true)

// Cyan is the Claude-style soft-cyan (color "81" / #5fafff) used for
// links, headings, code-block borders, and the rail cursor stripe.
var Cyan = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))

// CyanBold is Cyan + bold — the tool-call box header glyph.
var CyanBold = Cyan.Bold(true)

// Bold is the unstyled bold style — emphasis without color.
var Bold = lipgloss.NewStyle().Bold(true)

// Warn is the yellow attention style — "scrolled up" hint, idle-flag
// ⚡ in TopStatus, etc.
var Warn = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
