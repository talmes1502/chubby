// Package model — banner_view.go: the colored two-line header rendered
// at the top of the conversation viewport. Mirrors Claude Code's UI:
// a session-color stripe + name + cwd + activity line that flips
// "Thinking… (Xs)" → "Generating… (Xs · ↑ Nk · thought for Zs)" once
// text streaming begins.
package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/USER/chubby/tui/internal/views"
)

// renderSessionBanner builds the colored top-of-viewport header. The
// banner is a TWO-LINE block:
//
//	┃ <name>  ●  <cwd> · <kind>
//	  ✢ Thinking… (1m 23s · ↑ 3.7k tokens)
//
// or, when not thinking but with usage available:
//
//	┃ <name>  ●  <cwd> · <kind> · idle
//	  ↑ 12.3k ↓ 2.0k tokens · cache 999
//
// The bar (U+2503) and session-name are rendered in the session's color
// (bold). The dot (U+25CF) acts as a swatch, also in the session's color.
// cwd / kind / status are dimmed so they read as metadata rather than
// content.
//
// scrolledUp adds a "· scrolled up · End to jump down" suffix in
// yellow so the user sees that the viewport is no longer pinned to
// the bottom even if the new-messages badge isn't visible (e.g. they
// scrolled up but no new messages have arrived yet).
//
// usage / thinkingStartedAt / generationStartedAt / isThinking /
// spinnerFrame drive the activity line — see buildBannerActivityLine.
func renderSessionBanner(
	s *Session,
	spinnerFrame int,
	scrolledUp bool,
	usage sessionUsage,
	thinkingStartedAt time.Time,
	generationStartedAt time.Time,
	isThinking bool,
) string {
	col := lipgloss.Color(s.Color)
	colorStyle := lipgloss.NewStyle().Foreground(col).Bold(true)
	dim := views.Dim
	bar := colorStyle.Render("┃")
	name := colorStyle.Render(s.Name)
	swatch := lipgloss.NewStyle().Foreground(col).Render("●")

	// Line 1: identity (bar/name/swatch + cwd · kind). When NOT
	// thinking we also append the static status here so the second
	// line is free to show usage totals without restating "idle".
	line1 := fmt.Sprintf("%s %s  %s  %s",
		bar, name, swatch,
		dim.Render(fmt.Sprintf("%s · %s", s.Cwd, s.Kind)))
	if !isThinking {
		line1 += dim.Render(" · " + string(s.Status))
	}
	if scrolledUp {
		hint := views.Warn.
			Render(" · scrolled up · End to jump down")
		line1 += hint
	}

	// Line 2: activity. Branches on isThinking + whether we have
	// recorded any usage at all. Returning just line1 is allowed when
	// there's nothing useful to show on line 2 (no tokens yet, idle).
	line2 := buildBannerActivityLine(s, spinnerFrame, usage,
		thinkingStartedAt, generationStartedAt, isThinking)
	if line2 == "" {
		return line1
	}
	return line1 + "\n  " + line2
}

// buildBannerActivityLine constructs the 2nd banner line. Returns ""
// when there's nothing to render (no usage data + not thinking) so
// the caller can fall back to the original single-line banner shape.
//
// While thinking, the line mirrors Claude Code's UI exactly:
//
//	✢ Thinking… (Xm Ys · ↑ Nk tokens)
//	✢ Generating… (Xm Ys · ↑ Nk tokens · thought for Zs)
//
// The "Generating…" label and "thought for" suffix kick in once we've
// observed an assistant text block since thinkingStartedAt — that's
// the moment text streaming actually started, with everything before
// it being extended-thinking blocks.
func buildBannerActivityLine(
	s *Session,
	spinnerFrame int,
	usage sessionUsage,
	thinkingStartedAt time.Time,
	generationStartedAt time.Time,
	isThinking bool,
) string {
	_ = spinnerFrame // glyph is now static (✢) — keep the param for callers
	dim := views.Dim
	hasUsage := usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.CacheReadInputTokens > 0
	if !isThinking && !hasUsage {
		return ""
	}
	if isThinking {
		var elapsed time.Duration
		if !thinkingStartedAt.IsZero() {
			elapsed = time.Since(thinkingStartedAt)
		}
		// Use the input-token count (prompt size) since that's what
		// Claude's own banner shows. The output count is interesting
		// but Claude reserves it for the "↓" suffix during streaming —
		// we can revisit if users ask for it.
		tokensStr := views.FormatTokens(usage.InputTokens)
		parts := []string{
			views.FormatElapsed(elapsed),
			"↑ " + tokensStr + " tokens",
		}
		label := "Thinking…"
		if !generationStartedAt.IsZero() && !thinkingStartedAt.IsZero() {
			label = "Generating…"
			thoughtFor := generationStartedAt.Sub(thinkingStartedAt)
			if thoughtFor > 0 {
				parts = append(parts,
					"thought for "+views.FormatElapsed(thoughtFor))
			}
		}
		colorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(s.Color)).Bold(true)
		return fmt.Sprintf(
			"%s %s %s",
			colorStyle.Render("✢"),
			label,
			dim.Render("("+strings.Join(parts, " · ")+")"),
		)
	}
	// Idle / awaiting — show static totals only.
	parts := []string{
		fmt.Sprintf("↑ %s", views.FormatTokens(usage.InputTokens)),
		fmt.Sprintf("↓ %s tokens", views.FormatTokens(usage.OutputTokens)),
	}
	if usage.CacheReadInputTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache %s",
			views.FormatTokens(usage.CacheReadInputTokens)))
	}
	return dim.Render(strings.Join(parts, " · "))
}
