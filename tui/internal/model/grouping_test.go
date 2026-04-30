package model

import (
	"reflect"
	"testing"
)

// GroupKey is now used only by the D10c migration and the spawn-modal
// pre-fill heuristic — auto-grouping is no longer a render-time
// concept. We keep these unit tests around to lock the migration's
// "first tag, falling back to cwd basename" semantics in place.

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

// TestBuildRailRows_CollapsedFolderHidesSessions: collapsed folder
// suppresses its sessions while still rendering the header.
func TestBuildRailRows_CollapsedFolderHidesSessions(t *testing.T) {
	sessions := []Session{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b"},
	}
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("g1", "1")
	folders.Assign("g2", "2")
	rows := BuildRailRows(sessions, sessions, map[string]bool{"g1": true}, folders)
	// Expected: [folder g1 (collapsed), folder g2, session b (in g2)].
	// No separator (no unfiled).
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Kind != RailRowFolder || rows[0].GroupName != "g1" {
		t.Fatalf("rows[0]: %+v", rows[0])
	}
	if rows[1].Kind != RailRowFolder || rows[1].GroupName != "g2" {
		t.Fatalf("rows[1]: %+v", rows[1])
	}
	if rows[2].Kind != RailRowSession || rows[2].Session.Name != "b" {
		t.Fatalf("rows[2]: %+v", rows[2])
	}
}

func TestBuildRailRows_SessionIdxPointsToOriginal(t *testing.T) {
	all := []Session{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b"},
		{ID: "3", Name: "c"},
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
