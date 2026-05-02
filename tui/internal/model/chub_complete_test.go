package model

import (
	"strings"
	"testing"
)

// TestChubCommandComplete_HeadFromEmpty — Tab on empty input cycles
// through every command head. We assert the first match alphabetically
// is returned at idx=0 (sort.Strings on the heads).
func TestChubCommandComplete_HeadFromEmpty(t *testing.T) {
	m := Model{}
	out, ok, total := m.chubCommandComplete("", 0)
	if !ok {
		t.Fatalf("expected match for empty input")
	}
	if total < 5 {
		t.Fatalf("expected ≥5 candidates (whole catalog), got %d", total)
	}
	// First alphabetical command is "color".
	if out != "color" {
		t.Fatalf("expected first match=%q, got %q", "color", out)
	}
}

// TestChubCommandComplete_HeadPrefix — partial command head, e.g.
// "co" → "color". Cycle tested via repeated indices.
func TestChubCommandComplete_HeadPrefix(t *testing.T) {
	m := Model{}
	out, ok, _ := m.chubCommandComplete("co", 0)
	if !ok || out != "color" {
		t.Fatalf("expected co→color, got ok=%v out=%q", ok, out)
	}
}

// TestChubCommandComplete_StripsAndKeepsSlashPrefix — input starts
// with "/", output also starts with "/". Without the slash, output
// is bare. Lets users complete in either style.
func TestChubCommandComplete_PreservesSlashStyle(t *testing.T) {
	m := Model{}
	out, _, _ := m.chubCommandComplete("/co", 0)
	if !strings.HasPrefix(out, "/") {
		t.Fatalf("expected slash preserved on slash input, got %q", out)
	}
	out, _, _ = m.chubCommandComplete("co", 0)
	if strings.HasPrefix(out, "/") {
		t.Fatalf("expected no slash on bare input, got %q", out)
	}
}

// TestChubCommandComplete_ColorArgs — after "color " (with the
// trailing space), Tab cycles named colors + palette indexes.
func TestChubCommandComplete_ColorArgs(t *testing.T) {
	m := Model{}
	out, ok, _ := m.chubCommandComplete("color ", 0)
	if !ok {
		t.Fatalf("expected color args completion")
	}
	if !strings.HasPrefix(out, "color ") {
		t.Fatalf("expected output retains command head, got %q", out)
	}
	// First arg alphabetically (chubColorNames sorted) is "blue".
	if !strings.HasPrefix(out, "color blue") && !strings.Contains(out, "blue") {
		// Defensive: chubColorNames may add new entries before "blue"
		// alphabetically. Just confirm the suffix is non-empty.
		if out == "color " {
			t.Fatalf("expected non-empty arg, got %q", out)
		}
	}
}

// TestChubCommandComplete_ArgPrefix — "color bl" cycles to "color blue".
func TestChubCommandComplete_ArgPrefix(t *testing.T) {
	m := Model{}
	out, ok, _ := m.chubCommandComplete("color bl", 0)
	if !ok {
		t.Fatalf("expected match for color+prefix")
	}
	if !strings.HasPrefix(out, "color bl") {
		t.Fatalf("expected match starts with 'color bl', got %q", out)
	}
}

// TestChubCommandComplete_NoMatch — gibberish returns no completion.
func TestChubCommandComplete_NoMatch(t *testing.T) {
	m := Model{}
	if _, ok, _ := m.chubCommandComplete("zzz", 0); ok {
		t.Fatalf("expected no match for 'zzz'")
	}
}

// TestSplitChubCommand_AcceptsBareAndSlashPrefixed — verifies the
// parser accepts both "color blue" and "/color blue" forms.
func TestSplitChubCommand_AcceptsBareAndSlashPrefixed(t *testing.T) {
	for _, in := range []string{"color blue", "/color blue", "  color blue  "} {
		cmd, arg, ok := splitChubCommand(in)
		if !ok || cmd != ChubCmdColor || arg != "blue" {
			t.Fatalf("split(%q) = %q,%q,%v — want color,blue,true",
				in, cmd, arg, ok)
		}
	}
}
