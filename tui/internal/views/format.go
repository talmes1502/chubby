// Package views: format.go holds presentation helpers for the
// per-session banner — token counts, elapsed-time, the rotating
// "thinking" status text, and the animated activity slider.
//
// These are split into views/ rather than model/ so they can be unit
// tested without bringing in the bubbletea Model. The model only
// passes precomputed values (samples, elapsed, isThinking) into the
// banner renderer, which calls these helpers.
package views

import (
	"fmt"
	"strings"
	"time"
)

// FormatTokens turns a raw token count into a compact human-readable
// label. 12345 → "12.3k", 1234567 → "1.2M". Counts under 1000 render
// as the bare integer so a fresh session shows "0" not "0.0k".
func FormatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
}

// FormatElapsed renders a duration in the compact "Xm Ys" / "Xh Ym"
// form used for the thinking-elapsed counter. Sub-minute durations
// collapse to a single "Ns" so the banner doesn't waste real estate
// on a leading "0m " for the common case.
func FormatElapsed(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	secs %= 60
	if mins < 60 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	hours := mins / 60
	mins %= 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// UsageSample is a single (timestamp, output-token-count) data point
// fed to TokensPerSecond. The model maintains a small ring of these
// per session so the banner can show a rolling tokens/sec rate.
type UsageSample struct {
	Ts           time.Time
	OutputTokens int
}

// TokensPerSecond computes tokens/sec across the most recent
// ``window`` of samples. We pick the OLDEST sample whose ts is still
// within the window and divide the (last - first) delta by the
// observed gap. Returns 0 when we don't have enough data, when the
// window-bounded slice is empty, or when the gap is degenerate.
func TokensPerSecond(samples []UsageSample, now time.Time, window time.Duration) float64 {
	if len(samples) < 2 {
		return 0
	}
	cutoff := now.Add(-window)
	var first UsageSample
	found := false
	for _, s := range samples {
		if s.Ts.After(cutoff) {
			first = s
			found = true
			break
		}
	}
	if !found {
		return 0
	}
	last := samples[len(samples)-1]
	dur := last.Ts.Sub(first.Ts).Seconds()
	if dur <= 0 {
		return 0
	}
	return float64(last.OutputTokens-first.OutputTokens) / dur
}

// RenderSlider produces a Unicode block-cell slider whose fill
// proportion tracks tokens/sec, capped at 100 t/s = full width.
// Filled cells use ▰ and empty cells use ▱, both fixed-width so the
// total visual length doesn't shift as the rate changes.
func RenderSlider(tokensPerSec float64, width int) string {
	if width < 1 {
		width = 10
	}
	fill := tokensPerSec / 100.0
	if fill > 1.0 {
		fill = 1.0
	}
	if fill < 0 {
		fill = 0
	}
	cells := int(fill * float64(width))
	if cells > width {
		cells = width
	}
	if cells < 0 {
		cells = 0
	}
	return strings.Repeat("▰", cells) + strings.Repeat("▱", width-cells)
}

// ThinkingStatusText picks one of {thinking, cogitating, still
// thinking, cooking, almost done} based on how long the session has
// been thinking and the current tokens/sec rate. The "almost done"
// branch wins when output rate falls below 5 t/s after a 3-second
// warmup — a stalled stream typically means the assistant is wrapping
// up, not idling forever.
func ThinkingStatusText(elapsed time.Duration, tokensPerSec float64) string {
	if tokensPerSec < 5 && elapsed > 3*time.Second {
		return "almost done"
	}
	secs := elapsed.Seconds()
	switch {
	case secs < 5:
		return "thinking"
	case secs < 15:
		return "cogitating"
	case secs < 60:
		return "still thinking"
	default:
		return "cooking"
	}
}
