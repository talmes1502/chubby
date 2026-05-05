// Package model — slash_complete.go: /command autocompletion shared by
// the compose bar and the broadcast textarea.
//
// This complements complete.go (which handles trailing @-name
// autocomplete on session names) — same Tab-cycle pattern, but the
// catalog comes from views.SlashCommands and the syntax is
// "/<cmd>" or "/<cmd> <arg>".
package model

import (
	"regexp"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// slashCmdRe matches a fully-typed slash command name with no argument
// yet — i.e. the user is still typing the command itself.
//
// Anchored to the entire input: we only autocomplete when the slash
// command is the only thing in the buffer (no leading prose). This
// matches Claude Code's own conventions: /commands stand alone.
var slashCmdRe = regexp.MustCompile(`^/([a-zA-Z][a-zA-Z0-9-]*)?$`)

// slashArgRe matches "/<cmd> <partial-arg>" — i.e. the command name is
// settled and the user is now typing an argument.
var slashArgRe = regexp.MustCompile(`^/([a-zA-Z][a-zA-Z0-9-]*) ([^\s]*)$`)

// trySlashComplete attempts to complete a /command or its argument at
// the END of value. Returns (newValue, ok). If not at a slash, returns
// (value, false) so caller can fall through.
//
// Cycling: completionIndex is taken modulo the candidate count. With
// one match it just snaps; with many, repeated Tab presses by the
// caller (incrementing completionIndex each time) cycle through.
//
// On a name-with-args completion we append a trailing space so the user
// can immediately Tab again to cycle through the args. On a name with
// no args we also append a space (Claude's convention — ready to type
// or send). On an arg completion we do NOT append a space.
func trySlashComplete(value string, completionIndex int) (string, bool) {
	if m := slashCmdRe.FindStringSubmatch(value); m != nil {
		partial := m[1]
		matches := views.MatchSlashCommands(partial)
		if len(matches) == 0 {
			return value, false
		}
		pick := matches[completionIndex%len(matches)]
		// Trailing space lets the user keep typing or Tab again for an arg.
		return "/" + pick.Name + " ", true
	}
	if m := slashArgRe.FindStringSubmatch(value); m != nil {
		cmdName, partial := m[1], m[2]
		args := views.MatchSlashArg(cmdName, partial)
		if len(args) == 0 {
			return value, false
		}
		pick := args[completionIndex%len(args)]
		// No trailing space: arg is the terminal token.
		return "/" + cmdName + " " + pick, true
	}
	return value, false
}

// slashGhost returns the dim suffix to render after value as ghost
// text: shows the next completion match for the partial /command or
// arg. Empty string means no ghost.
func slashGhost(value string) string {
	if m := slashCmdRe.FindStringSubmatch(value); m != nil {
		partial := m[1]
		if partial == "" {
			// Don't surface a ghost for the bare "/" — too noisy, the
			// help overlay covers that.
			return ""
		}
		matches := views.MatchSlashCommands(partial)
		if len(matches) == 0 {
			return ""
		}
		pick := matches[0].Name
		if len(pick) <= len(partial) {
			return ""
		}
		return pick[len(partial):]
	}
	if m := slashArgRe.FindStringSubmatch(value); m != nil {
		cmdName, partial := m[1], m[2]
		if partial == "" {
			return ""
		}
		args := views.MatchSlashArg(cmdName, partial)
		if len(args) == 0 {
			return ""
		}
		pick := args[0]
		if len(pick) <= len(partial) {
			return ""
		}
		return pick[len(partial):]
	}
	return ""
}
