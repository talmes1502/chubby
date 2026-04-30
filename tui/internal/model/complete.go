// Package model — complete.go: @name autocompletion for the compose bar.
//
// The user can prefix a message with @<name> to redirect it to another
// session. Tab completes the partial name. If multiple sessions match,
// repeated Tab cycles through them.
package model

import (
	"regexp"
	"sort"
	"strings"
)

// trailingAtRe finds a trailing "@<partial>" anchored either at the
// start of input or after whitespace. The captured group is the partial
// (may be empty: "@" alone with no chars after).
//
// We allow letters, digits, and -_ in session names — matching the
// daemon's session-name conventions.
var trailingAtRe = regexp.MustCompile(`(?:^|\s)@([A-Za-z0-9_-]*)$`)

// extractTrailingAt returns (partial, startIdx, ok). startIdx is the
// index of '@' inside `input`, useful for splicing the completion in.
// ok is false if no trailing @-mention was found.
func extractTrailingAt(input string) (string, int, bool) {
	loc := trailingAtRe.FindStringSubmatchIndex(input)
	if loc == nil {
		return "", -1, false
	}
	// loc[2]:loc[3] is the partial group; the '@' is at loc[2]-1.
	at := loc[2] - 1
	partial := input[loc[2]:loc[3]]
	return partial, at, true
}

// matchSessionNames returns session names matching `partial` as a
// case-insensitive prefix, sorted alphabetically (case-insensitive).
// An empty partial returns every name.
func matchSessionNames(sessions []Session, partial string) []string {
	pl := strings.ToLower(partial)
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if pl == "" || strings.HasPrefix(strings.ToLower(s.Name), pl) {
			out = append(out, s.Name)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

// completeAt rewrites `input` by replacing the trailing "@<partial>"
// with "@<full> ". Returns the new string. If `input` has no trailing
// @-mention, returns input unchanged and ok=false.
func completeAt(input, full string) (string, bool) {
	_, at, ok := extractTrailingAt(input)
	if !ok {
		return input, false
	}
	return input[:at] + "@" + full + " ", true
}
