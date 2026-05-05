package model

import (
	"testing"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// nextCloneName picks the first free "<base>-N" name with N>=2,
// trimming any existing "-N" suffix so successive clones produce
// monotonically increasing numbers rather than nested -2-2-2.
func TestNextCloneName_FromFreshBase(t *testing.T) {
	m := Model{sessions: []Session{
		{ID: "a", Name: "web"},
	}}
	got := m.nextCloneName("web")
	if got != "web-2" {
		t.Fatalf("first clone of 'web' should be 'web-2'; got %q", got)
	}
}

func TestNextCloneName_SkipsTakenSuffixes(t *testing.T) {
	m := Model{sessions: []Session{
		{ID: "a", Name: "web"},
		{ID: "b", Name: "web-2"},
		{ID: "c", Name: "web-3"},
	}}
	got := m.nextCloneName("web")
	if got != "web-4" {
		t.Fatalf("with web/web-2/web-3 taken, next should be web-4; got %q", got)
	}
}

// Cloning ``web-2`` should produce ``web-3``, not ``web-2-2``, so
// chains of clones stay readable.
func TestNextCloneName_StripsExistingSuffix(t *testing.T) {
	m := Model{sessions: []Session{
		{ID: "a", Name: "web"},
		{ID: "b", Name: "web-2"},
	}}
	got := m.nextCloneName("web-2")
	if got != "web-3" {
		t.Fatalf("cloning 'web-2' should yield 'web-3'; got %q", got)
	}
}

// A name like ``feature-x`` (where the suffix isn't numeric) keeps
// the whole name as the base — ``feature-x-2``.
func TestNextCloneName_KeepsHyphenInRootIfNotNumeric(t *testing.T) {
	m := Model{sessions: []Session{
		{ID: "a", Name: "feature-x"},
	}}
	got := m.nextCloneName("feature-x")
	if got != "feature-x-2" {
		t.Fatalf("non-numeric suffix kept; got %q", got)
	}
}

func TestDoChubClone_NilWhenNoFocusedSession(t *testing.T) {
	m := Model{
		sessions: nil,
		focused:  -1,
		compose:  views.NewCompose(),
	}
	if cmd := m.doChubClone(); cmd != nil {
		t.Fatalf("clone with no focused session should return nil cmd")
	}
}
