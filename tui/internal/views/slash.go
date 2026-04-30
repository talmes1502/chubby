// Package views — slash.go: catalog of Claude Code's built-in
// /commands plus prefix-match helpers used by the compose-bar and
// broadcast textarea autocomplete.
package views

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// SlashCommand is one of Claude Code's built-in /commands.
type SlashCommand struct {
	Name        string   // e.g. "model"
	Description string   // human-readable hint
	Args        []string // empty = takes no arg; non-empty = autocomplete suggestions for the arg
}

// SlashCommands is the static catalog. Kept short and Claude-Code
// specific: these are the commands the daemon actually understands when
// proxied through inject. The "(chub)" prefix marks commands intercepted
// by the TUI itself rather than forwarded to Claude.
var SlashCommands = []SlashCommand{
	{"model", "Switch the Claude model for this session",
		[]string{"opus", "sonnet", "haiku",
			"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"}},
	{"clear", "Clear the conversation history", nil},
	{"compact", "Compact memory / summarize old turns", nil},
	{"exit", "Exit the session", nil},
	{"help", "Show Claude's built-in help", nil},
	{"login", "Re-authenticate", nil},
	{"status", "Show session status", nil},
	{"init", "Initialize a CLAUDE.md", nil},
	// chub-side commands — intercepted by the TUI; never sent to Claude.
	// The hex codes mirror src/chub/daemon/colors.py PALETTE.
	{"color", "(chub) recolor the focused session", []string{
		"#5fafff", "#ff8787", "#87d787", "#ffaf5f", "#d787d7",
		"#5fd7d7", "#d7d787", "#af87ff", "#ff5faf",
	}},
	{"rename", "(chub) rename the focused session", nil},
	{"tag", "(chub) modify session tags (e.g. /tag +foo -bar)", nil},
}

// MatchSlashCommands returns commands whose Name starts with prefix
// (case-insensitive), sorted alphabetically. An empty prefix matches
// every command.
func MatchSlashCommands(prefix string) []SlashCommand {
	pl := strings.ToLower(prefix)
	out := make([]SlashCommand, 0, len(SlashCommands))
	for _, c := range SlashCommands {
		if pl == "" || strings.HasPrefix(strings.ToLower(c.Name), pl) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// MatchSlashArg returns args of the named command whose value starts
// with prefix (case-insensitive). If the command isn't known or has no
// args, returns nil.
func MatchSlashArg(cmdName, prefix string) []string {
	c := FindSlashCommand(cmdName)
	if c == nil || len(c.Args) == 0 {
		return nil
	}
	pl := strings.ToLower(prefix)
	out := make([]string, 0, len(c.Args))
	for _, a := range c.Args {
		if pl == "" || strings.HasPrefix(strings.ToLower(a), pl) {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

// RenderSlashPopup draws the Claude-style autocomplete popup below the
// compose bar. cursor is the highlighted index; w is the available
// width (the popup will fit within it). Returns "" when matches is
// empty so callers can JoinVertical unconditionally.
//
// Layout: " /name  description" — name column is left-aligned and
// padded to the longest match (capped at 30 cols); description is
// truncated with an ellipsis when it can't fit. The highlighted row
// is rendered with a dim background so it reads as "selected" without
// stealing the eye.
func RenderSlashPopup(matches []SlashCommand, cursor int, w int) string {
	if len(matches) == 0 {
		return ""
	}
	// Find longest "/<name>" for column alignment, capped so a stray
	// long command doesn't squeeze the description column to zero.
	nameW := 0
	for _, c := range matches {
		n := len("/" + c.Name)
		if n > nameW {
			nameW = n
		}
	}
	if nameW > 30 {
		nameW = 30
	}
	// 1 leading space + nameW + 3 separator + descW = w (rough budget).
	descW := w - nameW - 4
	if descW < 10 {
		descW = 10
	}

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("248"))
	cursorStyle := lipgloss.NewStyle().Background(lipgloss.Color("237"))

	var b strings.Builder
	for i, c := range matches {
		name := "/" + c.Name
		if len(name) > nameW {
			name = name[:nameW-1] + "…"
		}
		desc := c.Description
		if rs := []rune(desc); len(rs) > descW {
			desc = string(rs[:descW-1]) + "…"
		}
		// Lay out the raw text first so column widths are correct, then
		// re-style each segment. We can't style first because lipgloss
		// adds ANSI escapes that would throw off %-*s padding.
		nameCol := fmt.Sprintf(" %-*s", nameW, name)
		descCol := "   " + desc
		line := nameStyle.Render(nameCol) + descStyle.Render(descCol)
		if i == cursor {
			line = cursorStyle.Render(line)
		}
		b.WriteString(line)
		if i < len(matches)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// FindSlashCommand returns the SlashCommand by name (case-insensitive)
// or nil if no such command exists.
func FindSlashCommand(name string) *SlashCommand {
	nl := strings.ToLower(name)
	for i := range SlashCommands {
		if strings.ToLower(SlashCommands[i].Name) == nl {
			return &SlashCommands[i]
		}
	}
	return nil
}
