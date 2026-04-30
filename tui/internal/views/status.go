// Package views — status.go: context-aware status bar rendered at the
// bottom of every view, plus the minimal top-of-screen header. Both
// follow the conventional TUI pattern (vim/lazygit/k9s): one-line dim
// keybinding hints scoped to the current mode and focus.
package views

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// StatusMode mirrors model.Mode. Defined here (instead of taking a
// model.Mode) because views cannot import model — that would create a
// cycle (model imports views). Callers in the model package cast their
// Mode to StatusMode; the iota order is intentionally identical.
type StatusMode int

const (
	StatusModeMain StatusMode = iota
	StatusModeBroadcast
	StatusModeGrep
	StatusModeHistory
	StatusModeReconnecting
	StatusModeSpawn
	StatusModeSearch
	StatusModeHelp
	StatusModeRename
)

// dimStyle is the shared dim foreground used for both the status bar
// and the placeholder run-id slot in the top header.
var dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// StatusBarText returns the keybinding hint string appropriate for the
// current mode, with extra context (compose-empty, broadcast field).
//
// composeHasText is only consulted for StatusModeMain.
// broadcastField is only consulted for StatusModeBroadcast (0,1,2).
//
// The returned string is already truncated to width with a trailing
// ellipsis if needed; pass width<=0 to skip truncation.
func StatusBarText(mode StatusMode, composeHasText bool, broadcastField int, width int) string {
	raw := rawStatusBar(mode, composeHasText, broadcastField)
	if width > 0 && lipgloss.Width(raw) > width {
		raw = truncateWithEllipsis(raw, width)
	}
	return dimStyle.Render(raw)
}

// rawStatusBar returns the un-styled, un-truncated status string.
// Separated so tests can match keywords without dealing with ANSI.
func rawStatusBar(mode StatusMode, composeHasText bool, broadcastField int) string {
	switch mode {
	case StatusModeMain:
		if composeHasText {
			return "Enter send · Shift+Enter newline · @name redirect · Tab complete · Esc clear"
		}
		return "Tab cycle · /cmd or @name (Tab completes) · Ctrl+B broadcast · / grep · Ctrl+H history · Ctrl+N new · Ctrl+P respawn · Ctrl+R rename · Ctrl+K search · Ctrl+Y copy · ? help · q quit"
	case StatusModeBroadcast:
		switch broadcastField {
		case 0:
			return "Tab fields · Space toggle · a all · n none · i invert · Esc cancel"
		case 1:
			return "Tab fields · Tab complete /cmd · Esc cancel"
		case 2:
			return "Tab fields · Enter send to selected · Esc cancel"
		}
		return "Tab fields · Esc cancel"
	case StatusModeGrep:
		return "↑↓ navigate · Enter jump · Esc back"
	case StatusModeHistory:
		return "Tab columns · ↑↓ select · Enter open · Esc back"
	case StatusModeSpawn:
		return "Tab switch field · Enter spawn · Esc cancel · ~ expands home"
	case StatusModeSearch:
		return "type to filter · Enter keep · Esc clear"
	case StatusModeRename:
		return "Enter to apply · Esc cancel"
	case StatusModeHelp:
		return "(any key dismisses)"
	case StatusModeReconnecting:
		return "connecting to chubd... · Ctrl+C quit"
	}
	return ""
}

// TopStatus renders the minimal one-line header above the main view.
// hubRunShort may be empty (we don't currently fetch the hub-run id
// from the daemon — the caller passes "" and we omit that segment).
// idleCount is the number of sessions currently in awaiting_user.
func TopStatus(hubRunShort string, sessionCount, idleCount, width int) string {
	var s string
	if hubRunShort != "" {
		s = fmt.Sprintf("chub · %s · %d sessions", hubRunShort, sessionCount)
	} else {
		s = fmt.Sprintf("chub · %d sessions", sessionCount)
	}
	if idleCount > 0 {
		s += fmt.Sprintf(" · %d idle ⚡", idleCount)
	}
	if width > 0 && lipgloss.Width(s) > width {
		s = truncateWithEllipsis(s, width)
	}
	return lipgloss.NewStyle().Bold(true).Render(s)
}

// truncateWithEllipsis trims s to at most width display columns,
// replacing the tail with a single ellipsis rune when truncation
// happens. Operates on runes (not bytes) so multibyte separators like
// "·" survive intact.
func truncateWithEllipsis(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return "…"
	}
	rs := []rune(s)
	// Walk runes accumulating display width; lipgloss.Width measures the
	// already-rendered string, so for plain text rune count == width
	// for the dot/letter inputs we use here. Keep it simple.
	if len(rs) <= width {
		return s
	}
	return string(rs[:width-1]) + "…"
}
