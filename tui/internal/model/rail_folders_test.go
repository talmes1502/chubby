package model

import (
	"reflect"
	"testing"
)

// TestBuildRailRows_FoldersAboveUnfiled: folders render first, then
// (when both folders and unfiled sessions exist) a separator, then a
// flat alphabetical list of unfiled sessions.
func TestBuildRailRows_FoldersAboveUnfiled(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
		{ID: "s2", Name: "worker", Tags: []string{"backend"}},
		{ID: "s3", Name: "ui", Tags: []string{"frontend"}},
	}
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("priority", "s1")

	rows := BuildRailRows(sessions, sessions, map[string]bool{}, folders)

	// Expected:
	// 0: folder header "priority"
	// 1: session s1 (api)  -- in priority
	// 2: separator (unfiled)
	// 3: session s2 (worker)  -- unfiled, alphabetical: ui then worker
	// 4: session s3 (ui)
	// Wait: alphabetical is "ui" before "worker", so:
	// 3: session s3 (ui)
	// 4: session s2 (worker)
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Kind != RailRowFolder || rows[0].GroupName != "priority" {
		t.Fatalf("rows[0] should be folder 'priority': %+v", rows[0])
	}
	if rows[1].Kind != RailRowSession || rows[1].Session.ID != "s1" {
		t.Fatalf("rows[1] should be session s1: %+v", rows[1])
	}
	if rows[2].Kind != RailRowUnfiledSeparator {
		t.Fatalf("rows[2] should be the unfiled separator: %+v", rows[2])
	}
	if rows[3].Kind != RailRowSession || rows[3].Session.ID != "s3" {
		t.Fatalf("rows[3] should be session s3 (ui): %+v", rows[3])
	}
	if rows[4].Kind != RailRowSession || rows[4].Session.ID != "s2" {
		t.Fatalf("rows[4] should be session s2 (worker): %+v", rows[4])
	}
}

// TestBuildRailRows_AssignedSessionDoesNotAlsoAppearInUnfiled:
// putting a session in a folder removes it from the unfiled list so
// it doesn't render twice.
func TestBuildRailRows_AssignedSessionDoesNotAlsoAppearInUnfiled(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "api"},
		{ID: "s2", Name: "worker"},
	}
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("hot", "s1")

	rows := BuildRailRows(sessions, sessions, map[string]bool{}, folders)

	// Walk the rows: track which folder each session row belongs to.
	gotInHot := []string{}
	gotUnfiled := []string{}
	for _, r := range rows {
		if r.Kind != RailRowSession {
			continue
		}
		if r.GroupName == "hot" {
			gotInHot = append(gotInHot, r.Session.ID)
		} else if r.GroupName == "" {
			gotUnfiled = append(gotUnfiled, r.Session.ID)
		}
	}
	if !reflect.DeepEqual(gotInHot, []string{"s1"}) {
		t.Fatalf("hot folder should have only s1, got %v", gotInHot)
	}
	if !reflect.DeepEqual(gotUnfiled, []string{"s2"}) {
		t.Fatalf("unfiled section should have only s2, got %v", gotUnfiled)
	}
}

// TestBuildRailRows_FoldersAlphabetical: folder headers sort
// alphabetically among themselves.
func TestBuildRailRows_FoldersAlphabetical(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "a"},
		{ID: "s2", Name: "b"},
		{ID: "s3", Name: "c"},
	}
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("zeta", "s1")
	folders.Assign("alpha", "s2")
	folders.Assign("Mu", "s3")

	rows := BuildRailRows(sessions, sessions, map[string]bool{}, folders)
	folderNames := []string{}
	for _, r := range rows {
		if r.Kind == RailRowFolder {
			folderNames = append(folderNames, r.GroupName)
		}
	}
	want := []string{"alpha", "Mu", "zeta"}
	if !reflect.DeepEqual(folderNames, want) {
		t.Fatalf("folder order: got %v want %v", folderNames, want)
	}
}

// TestBuildRailRows_CollapsedFolderHidesItsSessions: a collapsed
// folder header still renders (so the user can re-expand) but its
// assigned sessions are skipped.
func TestBuildRailRows_CollapsedFolderHidesItsSessions(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "a"},
		{ID: "s2", Name: "b"},
	}
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("hot", "s1")
	folders.Assign("hot", "s2")

	rows := BuildRailRows(sessions, sessions, map[string]bool{"hot": true}, folders)
	if len(rows) != 1 {
		t.Fatalf("expected just the folder header, got %d rows: %+v", len(rows), rows)
	}
	if rows[0].Kind != RailRowFolder {
		t.Fatalf("first row should be folder, got %+v", rows[0])
	}
}

// TestBuildRailRows_EmptyFoldersStateIsFlatList: with no folders, the
// rail is a flat alphabetical list of every visible session — no
// auto-grouping headers, no separator.
func TestBuildRailRows_EmptyFoldersStateIsFlatList(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "Worker", Tags: []string{"backend"}},
		{ID: "s2", Name: "api", Tags: []string{"backend"}},
		{ID: "s3", Name: "ui", Tags: []string{"frontend"}},
	}
	rows := BuildRailRows(sessions, sessions, map[string]bool{}, FoldersState{})

	// Expect: every row is RailRowSession, alphabetical (case-insensitive)
	// by Name → api, ui, Worker.
	wantNames := []string{"api", "ui", "Worker"}
	if len(rows) != len(wantNames) {
		t.Fatalf("expected %d rows, got %d: %+v", len(wantNames), len(rows), rows)
	}
	for i, r := range rows {
		if r.Kind != RailRowSession {
			t.Fatalf("rows[%d] should be a session, got %+v", i, r)
		}
		if r.Session.Name != wantNames[i] {
			t.Fatalf("rows[%d].Name = %q, want %q", i, r.Session.Name, wantNames[i])
		}
		// GroupName "" identifies these as unfiled.
		if r.GroupName != "" {
			t.Fatalf("rows[%d].GroupName = %q, want empty (unfiled)", i, r.GroupName)
		}
	}
}

// TestBuildRailRows_NoSeparatorWhenOnlyFolders: no separator row when
// every visible session is in a folder.
func TestBuildRailRows_NoSeparatorWhenOnlyFolders(t *testing.T) {
	sessions := []Session{{ID: "s1", Name: "a"}}
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("hot", "s1")
	rows := BuildRailRows(sessions, sessions, map[string]bool{}, folders)
	for _, r := range rows {
		if r.Kind == RailRowUnfiledSeparator {
			t.Fatalf("did not expect separator row, got rows=%+v", rows)
		}
	}
}

// TestBuildRailRows_NoSeparatorWhenOnlyUnfiled: no separator row when
// there are no folders at all.
func TestBuildRailRows_NoSeparatorWhenOnlyUnfiled(t *testing.T) {
	sessions := []Session{{ID: "s1", Name: "a"}, {ID: "s2", Name: "b"}}
	rows := BuildRailRows(sessions, sessions, map[string]bool{}, FoldersState{})
	for _, r := range rows {
		if r.Kind == RailRowUnfiledSeparator {
			t.Fatalf("did not expect separator row, got rows=%+v", rows)
		}
	}
}
