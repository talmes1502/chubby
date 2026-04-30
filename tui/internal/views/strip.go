// Package views — strip.go: ANSI escape sanitization for the live viewport.
//
// The wrapped Claude TUI emits cursor-positioning, mode-set, OSC, and other
// terminal-control escapes that are meaningful only to a real terminal
// emulator. Rendering them inside Bubble Tea's lipgloss viewport produces
// gibberish like "\x1b[2C\x1b[3A\x1b[?2026l...". We strip everything except
// SGR (color/style) so the user sees readable text + colors.
package views

import "regexp"

// CSI sequences ending in a final byte in [0x40-0x7E] (ASCII '@'..'~').
// We match the whole sequence and then keep only those terminating in 'm'
// (SGR — colors and text styling).
var csiRe = regexp.MustCompile(`\x1b\[[0-9;:?<>!]*[\x40-\x7e]`)

// OSC sequences: ESC ] ... BEL  or  ESC ] ... ESC \.
// Used for window titles, hyperlinks, etc. — never useful here.
var oscRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// Standalone ESC-prefixed two-byte controls we want to drop:
//
//	ESC 7 (DECSC, save cursor)
//	ESC 8 (DECRC, restore cursor)
//	ESC = / ESC > (keypad mode)
//	ESC c        (full reset)
var escPairRe = regexp.MustCompile(`\x1b[78=>c]`)

// StripCursorEscapes removes terminal-control escapes that are not SGR.
// SGR sequences (ending in 'm') are preserved so colors keep working.
//
// Rules:
//  1. Every CSI sequence ending in a final byte other than 'm' is dropped.
//  2. OSC sequences (ESC ] ... BEL / ESC ] ... ST) are dropped.
//  3. ESC 7 / 8 / = / > / c are dropped.
//
// Anything that doesn't match any of these patterns is left alone — better
// to leak the occasional stray escape than to mangle real text.
func StripCursorEscapes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	// Drop OSC first: the ST form (ESC \) contains an ESC byte that would
	// otherwise confuse the CSI matcher if we ran it later.
	out := oscRe.ReplaceAll(b, nil)
	out = csiRe.ReplaceAllFunc(out, func(m []byte) []byte {
		// Keep SGR (final byte 'm'); drop everything else.
		if len(m) > 0 && m[len(m)-1] == 'm' {
			return m
		}
		return nil
	})
	out = escPairRe.ReplaceAll(out, nil)
	return out
}
