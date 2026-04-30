package model

import (
	"reflect"
	"testing"
)

func TestExtractTrailingAt_AtStart(t *testing.T) {
	p, idx, ok := extractTrailingAt("@api")
	if !ok || p != "api" || idx != 0 {
		t.Fatalf("got partial=%q idx=%d ok=%v, want partial='api' idx=0 ok=true", p, idx, ok)
	}
}

func TestExtractTrailingAt_AfterSpace(t *testing.T) {
	p, idx, ok := extractTrailingAt("hello @wo")
	if !ok || p != "wo" || idx != 6 {
		t.Fatalf("got partial=%q idx=%d ok=%v", p, idx, ok)
	}
}

func TestExtractTrailingAt_NoMention(t *testing.T) {
	if _, _, ok := extractTrailingAt("hello world"); ok {
		t.Fatal("expected no mention")
	}
}

func TestExtractTrailingAt_NotTrailing(t *testing.T) {
	// @api in the middle, then more text after — should NOT match (not trailing).
	if _, _, ok := extractTrailingAt("@api hi there"); ok {
		t.Fatal("expected no trailing mention; the '@' is followed by content already")
	}
}

func TestExtractTrailingAt_EmailLike(t *testing.T) {
	// Email-like "foo@bar" must NOT match because '@' is preceded by a letter,
	// not whitespace or start-of-string.
	if _, _, ok := extractTrailingAt("send to foo@bar"); ok {
		t.Fatal("expected no match for email-like 'foo@bar'")
	}
}

func TestExtractTrailingAt_EmptyPartial(t *testing.T) {
	p, idx, ok := extractTrailingAt("@")
	if !ok || p != "" || idx != 0 {
		t.Fatalf("got partial=%q idx=%d ok=%v", p, idx, ok)
	}
}

func TestMatchSessionNames_PrefixCaseInsensitive(t *testing.T) {
	in := []Session{
		{Name: "API"}, {Name: "apiX"}, {Name: "worker"}, {Name: "ApiZ"},
	}
	got := matchSessionNames(in, "api")
	// case-insensitive: "api" < "apix" < "apiz" → API, apiX, ApiZ
	want := []string{"API", "apiX", "ApiZ"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMatchSessionNames_EmptyPartialMatchesAll(t *testing.T) {
	in := []Session{{Name: "b"}, {Name: "a"}}
	got := matchSessionNames(in, "")
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCompleteAt_StartOfInput(t *testing.T) {
	out, ok := completeAt("@a", "api")
	if !ok || out != "@api " {
		t.Fatalf("got %q ok=%v", out, ok)
	}
}

func TestCompleteAt_AfterSpace(t *testing.T) {
	out, ok := completeAt("hi @w", "worker")
	if !ok || out != "hi @worker " {
		t.Fatalf("got %q ok=%v", out, ok)
	}
}

func TestCompleteAt_NoMention(t *testing.T) {
	out, ok := completeAt("hello", "api")
	if ok || out != "hello" {
		t.Fatalf("got %q ok=%v", out, ok)
	}
}

func TestTryComplete_SingleMatch(t *testing.T) {
	m := Model{
		sessions: []Session{{Name: "api"}, {Name: "worker"}},
	}
	m.compose.SetValue("@a")
	if !m.tryComplete() {
		t.Fatal("expected completion")
	}
	if m.compose.Value() != "@api " {
		t.Fatalf("got %q", m.compose.Value())
	}
}

func TestTryComplete_CycleThroughMultiple(t *testing.T) {
	m := Model{
		sessions: []Session{{Name: "api"}, {Name: "apiX"}, {Name: "apiZ"}},
	}
	m.compose.SetValue("@ap")
	// First Tab → first match (alphabetical: api, apiX, apiZ).
	if !m.tryComplete() {
		t.Fatal("first tab failed")
	}
	if m.compose.Value() != "@api " {
		t.Fatalf("first cycle got %q", m.compose.Value())
	}
	// Now retype the partial to cycle.
	m.compose.SetValue("@ap")
	if !m.tryComplete() {
		t.Fatal("second tab failed")
	}
	got1 := m.compose.Value()
	m.compose.SetValue("@ap")
	if !m.tryComplete() {
		t.Fatal("third tab failed")
	}
	got2 := m.compose.Value()
	if got1 == got2 {
		t.Fatalf("cycle did not advance: both %q", got1)
	}
}

func TestTryComplete_NoMatchReturnsFalse(t *testing.T) {
	m := Model{sessions: []Session{{Name: "api"}}}
	m.compose.SetValue("@zzz")
	if m.tryComplete() {
		t.Fatal("expected no completion")
	}
}

func TestTryComplete_NoMentionReturnsFalse(t *testing.T) {
	m := Model{sessions: []Session{{Name: "api"}}}
	m.compose.SetValue("hello world")
	if m.tryComplete() {
		t.Fatal("expected no completion")
	}
}

func TestComposeGhost_ShowsRemainder(t *testing.T) {
	m := Model{sessions: []Session{{Name: "worker"}, {Name: "api"}}}
	m.compose.SetValue("@wo")
	got := m.composeGhost()
	if got != "rker" {
		t.Fatalf("got %q want 'rker'", got)
	}
}

func TestComposeGhost_NoneWhenNoPartial(t *testing.T) {
	m := Model{sessions: []Session{{Name: "api"}}}
	m.compose.SetValue("hello")
	if got := m.composeGhost(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestComposeGhost_NoneForExactMatch(t *testing.T) {
	m := Model{sessions: []Session{{Name: "api"}}}
	m.compose.SetValue("@api")
	if got := m.composeGhost(); got != "" {
		t.Fatalf("expected empty for exact match, got %q", got)
	}
}
