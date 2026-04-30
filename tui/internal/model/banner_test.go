package model

import (
	"strings"
	"testing"
	"time"
)

// TestBuildBannerActivityLine_Thinking — without an observed assistant
// text block yet, the banner should label the state "Thinking…" and
// omit the "thought for" suffix.
func TestBuildBannerActivityLine_Thinking(t *testing.T) {
	s := &Session{ID: "s1", Color: "#5fafff"}
	usage := sessionUsage{InputTokens: 3700}
	thinkStart := time.Now().Add(-12 * time.Second)
	got := buildBannerActivityLine(s, 0, usage, thinkStart, time.Time{}, true)
	if !strings.Contains(got, "Thinking…") {
		t.Fatalf("expected 'Thinking…' label, got %q", got)
	}
	if !strings.Contains(got, "✢") {
		t.Fatalf("expected sparkle glyph, got %q", got)
	}
	if !strings.Contains(got, "↑") {
		t.Fatalf("expected input-token arrow, got %q", got)
	}
	if strings.Contains(got, "thought for") {
		t.Fatalf("should NOT show 'thought for' before generation starts: %q", got)
	}
	if strings.Contains(got, "Generating") {
		t.Fatalf("should NOT show 'Generating' before text streaming: %q", got)
	}
}

// TestBuildBannerActivityLine_Generating — once generationStartedAt is
// set, label flips to "Generating…" and the "thought for" suffix
// reflects the (gen − think) duration.
func TestBuildBannerActivityLine_Generating(t *testing.T) {
	s := &Session{ID: "s1", Color: "#5fafff"}
	usage := sessionUsage{InputTokens: 3700}
	thinkStart := time.Now().Add(-30 * time.Second)
	genStart := thinkStart.Add(20 * time.Second)
	got := buildBannerActivityLine(s, 0, usage, thinkStart, genStart, true)
	if !strings.Contains(got, "Generating…") {
		t.Fatalf("expected 'Generating…' label, got %q", got)
	}
	if !strings.Contains(got, "thought for") {
		t.Fatalf("expected 'thought for' suffix, got %q", got)
	}
	if strings.Contains(got, "Thinking…") {
		t.Fatalf("should NOT show 'Thinking…' once generation has started: %q", got)
	}
}

// TestBuildBannerActivityLine_Idle — when not thinking and we have
// usage, the line shows static totals and contains no spinner glyph.
func TestBuildBannerActivityLine_Idle(t *testing.T) {
	s := &Session{ID: "s1", Color: "#5fafff"}
	usage := sessionUsage{InputTokens: 1000, OutputTokens: 200}
	got := buildBannerActivityLine(s, 0, usage, time.Time{}, time.Time{}, false)
	if !strings.Contains(got, "↑") || !strings.Contains(got, "↓") {
		t.Fatalf("expected idle line to show both arrows, got %q", got)
	}
	if strings.Contains(got, "✢") {
		t.Fatalf("should NOT show sparkle glyph when idle: %q", got)
	}
}

// TestBuildBannerActivityLine_NothingToShow — with no usage and not
// thinking, return "" so the caller can fall back to a single-line
// banner shape.
func TestBuildBannerActivityLine_NothingToShow(t *testing.T) {
	s := &Session{ID: "s1", Color: "#5fafff"}
	got := buildBannerActivityLine(s, 0, sessionUsage{}, time.Time{}, time.Time{}, false)
	if got != "" {
		t.Fatalf("expected empty when no usage + not thinking, got %q", got)
	}
}
