package model

import (
	"reflect"
	"testing"
)

func TestGroupKey_TagFirst(t *testing.T) {
	s := Session{Name: "api", Tags: []string{"backend", "x"}, Cwd: "/srv/api"}
	if got := GroupKey(s); got != "backend" {
		t.Fatalf("expected first tag, got %q", got)
	}
}

func TestGroupKey_FallbackToCwdBasename(t *testing.T) {
	s := Session{Name: "api", Cwd: "/Users/me/code/portfolio"}
	if got := GroupKey(s); got != "portfolio" {
		t.Fatalf("expected cwd basename, got %q", got)
	}
}

func TestGroupKey_Untitled(t *testing.T) {
	cases := map[string]Session{
		"empty": {Name: "x", Cwd: ""},
		"slash": {Name: "x", Cwd: "/"},
	}
	for label, s := range cases {
		if got := GroupKey(s); got != UntitledGroup {
			t.Errorf("%s: expected %q, got %q", label, UntitledGroup, got)
		}
	}
}

func TestGroupKey_EmptyTagFallsBack(t *testing.T) {
	// An empty-string tag must not become the group key — fall back to cwd.
	s := Session{Name: "x", Tags: []string{""}, Cwd: "/srv/api"}
	if got := GroupKey(s); got != "api" {
		t.Fatalf("expected 'api', got %q", got)
	}
}

func TestGroupSessions_AlphabeticalGroupsAndSessions(t *testing.T) {
	in := []Session{
		{ID: "1", Name: "Worker", Tags: []string{"backend"}},
		{ID: "2", Name: "api", Tags: []string{"backend"}},
		{ID: "3", Name: "portfolio", Cwd: "/u/me/portfolio"},
		{ID: "4", Name: "z-thing", Cwd: "/"},
	}
	groups := GroupSessions(in)
	// Group order: backend, portfolio, (untitled).
	wantGroups := []string{"backend", "portfolio", UntitledGroup}
	got := []string{}
	for _, g := range groups {
		got = append(got, g.Name)
	}
	if !reflect.DeepEqual(got, wantGroups) {
		t.Fatalf("group order: got %v want %v", got, wantGroups)
	}
	// Backend sessions: "api" before "Worker" (case-insensitive).
	if groups[0].Sessions[0].Name != "api" || groups[0].Sessions[1].Name != "Worker" {
		t.Fatalf("session order in backend group: %+v", groups[0].Sessions)
	}
}

func TestGroupSessions_UntitledLastEvenIfAlphabeticallyEarlier(t *testing.T) {
	in := []Session{
		{ID: "1", Name: "x", Cwd: "/"},               // (untitled)
		{ID: "2", Name: "y", Tags: []string{"alpha"}}, // alpha
	}
	groups := GroupSessions(in)
	if groups[0].Name != "alpha" {
		t.Fatalf("alpha should be first, got %q", groups[0].Name)
	}
	if groups[1].Name != UntitledGroup {
		t.Fatalf("untitled should be last, got %q", groups[1].Name)
	}
}

func TestBuildRailRows_CollapsedSkipsSessions(t *testing.T) {
	sessions := []Session{
		{ID: "1", Name: "a", Tags: []string{"g1"}},
		{ID: "2", Name: "b", Tags: []string{"g2"}},
	}
	rows := BuildRailRows(sessions, sessions, map[string]bool{"g1": true}, FoldersState{})
	// Expect: [header g1, header g2, session b]
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Kind != RailRowHeader || rows[0].GroupName != "g1" {
		t.Fatalf("rows[0]: %+v", rows[0])
	}
	if rows[1].Kind != RailRowHeader || rows[1].GroupName != "g2" {
		t.Fatalf("rows[1]: %+v", rows[1])
	}
	if rows[2].Kind != RailRowSession || rows[2].Session.Name != "b" {
		t.Fatalf("rows[2]: %+v", rows[2])
	}
}

func TestBuildRailRows_SessionIdxPointsToOriginal(t *testing.T) {
	all := []Session{
		{ID: "1", Name: "a", Tags: []string{"g"}},
		{ID: "2", Name: "b", Tags: []string{"g"}},
		{ID: "3", Name: "c", Tags: []string{"g"}},
	}
	// Pretend "b" is filtered out.
	visible := []Session{all[0], all[2]}
	rows := BuildRailRows(visible, all, map[string]bool{}, FoldersState{})
	got := []int{}
	for _, r := range rows {
		if r.Kind == RailRowSession {
			got = append(got, r.SessionIdx)
		}
	}
	want := []int{0, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session indices: got %v want %v", got, want)
	}
}
