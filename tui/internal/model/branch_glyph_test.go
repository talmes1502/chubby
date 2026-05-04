package model

import (
	"strings"
	"testing"
)

// branchGlyph returns "" for the "no info" cases so the rail row
// stays compact when nothing useful can be shown.
func TestBranchGlyph_HiddenWhenNoInfo(t *testing.T) {
	cases := []struct {
		name   string
		ahead  *int
		behind *int
	}{
		{"both nil (no upstream / not a repo)", nil, nil},
		{"both zero (in sync)", iptr(0), iptr(0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := branchGlyph(c.ahead, c.behind)
			if got != "" {
				t.Fatalf("expected empty glyph, got %q", got)
			}
		})
	}
}

func TestBranchGlyph_AheadOnly(t *testing.T) {
	got := branchGlyph(iptr(3), iptr(0))
	if got != "↑3" {
		t.Fatalf("ahead-only should render ↑N; got %q", got)
	}
}

func TestBranchGlyph_BehindOnly(t *testing.T) {
	got := branchGlyph(iptr(0), iptr(2))
	if got != "↓2" {
		t.Fatalf("behind-only should render ↓N; got %q", got)
	}
}

func TestBranchGlyph_BothDirections(t *testing.T) {
	got := branchGlyph(iptr(5), iptr(7))
	// Diverged branches show both arrows so the user knows a
	// straight push won't fast-forward.
	if got != "↑5↓7" {
		t.Fatalf("diverged should render ↑N↓M; got %q", got)
	}
}

func TestBranchGlyph_NilAheadWithBehind(t *testing.T) {
	// A daemon that only filled one of the two pointers (shouldn't
	// happen normally — they're set together — but defensive).
	got := branchGlyph(nil, iptr(4))
	if !strings.Contains(got, "↓4") {
		t.Fatalf("partial info should still render the available count; got %q", got)
	}
}

// intFromAny: the wire format passes JSON null for "no upstream" and
// JSON numbers as float64 (Go's default). The helper must round-trip
// both shapes correctly.
func TestIntFromAny_JSONNumber(t *testing.T) {
	got := intFromAny(float64(7))
	if got == nil || *got != 7 {
		t.Fatalf("float64(7) → *int(7); got %v", got)
	}
}

func TestIntFromAny_JSONNull(t *testing.T) {
	got := intFromAny(nil)
	if got != nil {
		t.Fatalf("nil → nil; got non-nil pointer to %d", *got)
	}
}

func TestIntFromAny_ZeroIsValid(t *testing.T) {
	// Zero is a valid count (in-sync branch); must not be confused
	// with "no info".
	got := intFromAny(float64(0))
	if got == nil {
		t.Fatalf("0 → *int(0); got nil")
	}
	if *got != 0 {
		t.Fatalf("expected 0, got %d", *got)
	}
}

func TestIntFromAny_UnknownTypeReturnsNil(t *testing.T) {
	// Defensive: a future schema change that puts a string in this
	// field shouldn't crash the renderer.
	got := intFromAny("not-a-number")
	if got != nil {
		t.Fatalf("string input should return nil; got *int(%d)", *got)
	}
}

func iptr(v int) *int {
	return &v
}
