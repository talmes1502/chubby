package model

import (
	"strings"
	"testing"
)

func TestPortsBadge_EmptyHidden(t *testing.T) {
	if got := portsBadge(nil); got != "" {
		t.Fatalf("nil ports → empty string; got %q", got)
	}
	if got := portsBadge([]SessionPort{}); got != "" {
		t.Fatalf("empty ports → empty string; got %q", got)
	}
}

func TestPortsBadge_SinglePort(t *testing.T) {
	got := portsBadge([]SessionPort{{Port: 3000, Pid: 42, Address: "127.0.0.1"}})
	// Order: globe glyph + space + ":3000".
	if !strings.Contains(got, ":3000") {
		t.Fatalf("expected :3000 in badge; got %q", got)
	}
	if !strings.Contains(got, "🌐") {
		t.Fatalf("expected globe glyph; got %q", got)
	}
}

func TestPortsBadge_TwoPortsBothShown(t *testing.T) {
	got := portsBadge([]SessionPort{
		{Port: 3000, Pid: 42},
		{Port: 5173, Pid: 43},
	})
	if !strings.Contains(got, ":3000") || !strings.Contains(got, ":5173") {
		t.Fatalf("both ports should appear; got %q", got)
	}
	// Should NOT have an overflow indicator.
	if strings.Contains(got, "+") {
		t.Fatalf("two ports shouldn't trigger +N overflow; got %q", got)
	}
}

func TestPortsBadge_OverflowSuffix(t *testing.T) {
	// Three ports → first two shown, +1 overflow.
	got := portsBadge([]SessionPort{
		{Port: 3000, Pid: 42},
		{Port: 3001, Pid: 43},
		{Port: 3002, Pid: 44},
	})
	if !strings.Contains(got, ":3000") || !strings.Contains(got, ":3001") {
		t.Fatalf("first two ports should appear; got %q", got)
	}
	if strings.Contains(got, ":3002") {
		t.Fatalf("third port should be in the +N overflow, not shown; got %q", got)
	}
	if !strings.Contains(got, "+1") {
		t.Fatalf("overflow indicator missing; got %q", got)
	}
}
