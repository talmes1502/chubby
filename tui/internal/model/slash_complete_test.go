package model

import "testing"

func TestTrySlashComplete_PrefixCompletesName(t *testing.T) {
	got, ok := trySlashComplete("/m", 0)
	if !ok || got != "/model " {
		t.Fatalf("got %q ok=%v, want '/model ' true", got, ok)
	}
}

func TestTrySlashComplete_BareSlashPicksFirst(t *testing.T) {
	// "/" alone returns the whole catalog; first alphabetically is "clear".
	got, ok := trySlashComplete("/", 0)
	if !ok || got != "/clear " {
		t.Fatalf("got %q ok=%v, want '/clear ' true", got, ok)
	}
}

func TestTrySlashComplete_ArgPrefix(t *testing.T) {
	got, ok := trySlashComplete("/model so", 0)
	if !ok || got != "/model sonnet" {
		t.Fatalf("got %q ok=%v, want '/model sonnet' true", got, ok)
	}
}

func TestTrySlashComplete_ArgFullPrefix(t *testing.T) {
	got, ok := trySlashComplete("/model claude-", 0)
	if !ok {
		t.Fatalf("expected ok, got %q", got)
	}
	// Cycle should reach all three claude-* IDs (claude-haiku, claude-opus, claude-sonnet).
	seen := map[string]bool{got: true}
	for i := 1; i < 6; i++ {
		next, ok2 := trySlashComplete("/model claude-", i)
		if !ok2 {
			t.Fatal("cycle returned ok=false")
		}
		seen[next] = true
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 distinct claude-* completions across the cycle, got %v", seen)
	}
}

func TestTrySlashComplete_CycleNames(t *testing.T) {
	// "/c" matches both "clear" and "compact". Two Tabs should give two
	// distinct completions.
	v1, ok1 := trySlashComplete("/c", 0)
	v2, ok2 := trySlashComplete("/c", 1)
	if !ok1 || !ok2 {
		t.Fatalf("expected both ok; got %q/%v %q/%v", v1, ok1, v2, ok2)
	}
	if v1 == v2 {
		t.Fatalf("cycle did not advance: both %q", v1)
	}
	if v1 != "/clear " && v1 != "/compact " {
		t.Fatalf("first cycle picked unexpected value %q", v1)
	}
}

func TestTrySlashComplete_NonSlashFallsThrough(t *testing.T) {
	got, ok := trySlashComplete("hello world", 0)
	if ok || got != "hello world" {
		t.Fatalf("got %q ok=%v, want passthrough", got, ok)
	}
}

func TestTrySlashComplete_SlashWithLeadingProseFallsThrough(t *testing.T) {
	// "/" only matches at the START of the buffer. Embedded slash should
	// not trigger completion — that's a literal slash in prose.
	got, ok := trySlashComplete("hello /m", 0)
	if ok || got != "hello /m" {
		t.Fatalf("got %q ok=%v, want passthrough", got, ok)
	}
}

func TestTrySlashComplete_UnknownCommandNoMatch(t *testing.T) {
	got, ok := trySlashComplete("/zzz", 0)
	if ok || got != "/zzz" {
		t.Fatalf("got %q ok=%v, want passthrough", got, ok)
	}
}

func TestTrySlashComplete_UnknownArgNoMatch(t *testing.T) {
	// Known command, unknown arg prefix.
	got, ok := trySlashComplete("/model zzz", 0)
	if ok || got != "/model zzz" {
		t.Fatalf("got %q ok=%v, want passthrough", got, ok)
	}
}

func TestSlashGhost_CommandPrefix(t *testing.T) {
	if g := slashGhost("/mo"); g != "del" {
		t.Fatalf("got %q want 'del'", g)
	}
}

func TestSlashGhost_NoneForBareSlash(t *testing.T) {
	if g := slashGhost("/"); g != "" {
		t.Fatalf("expected no ghost for bare '/', got %q", g)
	}
}

func TestSlashGhost_ArgPrefix(t *testing.T) {
	if g := slashGhost("/model so"); g != "nnet" {
		t.Fatalf("got %q want 'nnet'", g)
	}
}

func TestSlashGhost_NoneForExactMatch(t *testing.T) {
	// "/model" is an exact name — no remaining suffix, so no ghost.
	if g := slashGhost("/model"); g != "" {
		t.Fatalf("got %q want empty", g)
	}
}

func TestSlashGhost_NoneForNonSlash(t *testing.T) {
	if g := slashGhost("hello"); g != "" {
		t.Fatalf("got %q want empty", g)
	}
}
