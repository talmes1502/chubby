// Package views — slash.go: catalog of Claude Code's built-in
// /commands plus prefix-match helpers used by the compose-bar and
// broadcast textarea autocomplete.
package views

import (
	"sort"
	"strings"
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
