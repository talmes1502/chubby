package model

import (
	"reflect"
	"testing"
)

// TestBuildRailRows_FoldersAboveAutoGroups: a session in a folder
// renders under the folder header; sessions outside any folder fall
// back to the cwd/tag auto-grouping path. Folders always come before
// auto-groups.
func TestBuildRailRows_FoldersAboveAutoGroups(t *testing.T) {
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
	// 1: session s1 (api)
	// 2: header "backend"  (s2 only)
	// 3: session s2 (worker)
	// 4: header "frontend" (s3)
	// 5: session s3 (ui)
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Kind != RailRowFolder || rows[0].GroupName != "priority" {
		t.Fatalf("rows[0] should be folder 'priority': %+v", rows[0])
	}
	if rows[1].Kind != RailRowSession || rows[1].Session.ID != "s1" {
		t.Fatalf("rows[1] should be session s1: %+v", rows[1])
	}
	if rows[2].Kind != RailRowHeader || rows[2].GroupName != "backend" {
		t.Fatalf("rows[2] should be header 'backend': %+v", rows[2])
	}
	if rows[3].Kind != RailRowSession || rows[3].Session.ID != "s2" {
		t.Fatalf("rows[3] should be session s2: %+v", rows[3])
	}
	if rows[4].Kind != RailRowHeader || rows[4].GroupName != "frontend" {
		t.Fatalf("rows[4] should be header 'frontend': %+v", rows[4])
	}
	if rows[5].Kind != RailRowSession || rows[5].Session.ID != "s3" {
		t.Fatalf("rows[5] should be session s3: %+v", rows[5])
	}
}

// TestBuildRailRows_AssignedSessionDoesNotAlsoAppearInAutoGroup:
// putting a session in a folder removes it from its tag/cwd auto-group
// so it doesn't render twice.
func TestBuildRailRows_AssignedSessionDoesNotAlsoAppearInAutoGroup(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
		{ID: "s2", Name: "worker", Tags: []string{"backend"}},
	}
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("hot", "s1")

	rows := BuildRailRows(sessions, sessions, map[string]bool{}, folders)

	// "backend" auto-group should now have s2 only (not s1).
	gotInBackend := []string{}
	gotInHot := []string{}
	for _, r := range rows {
		if r.Kind != RailRowSession {
			continue
		}
		switch r.GroupName {
		case "backend":
			gotInBackend = append(gotInBackend, r.Session.ID)
		case "hot":
			gotInHot = append(gotInHot, r.Session.ID)
		}
	}
	if !reflect.DeepEqual(gotInBackend, []string{"s2"}) {
		t.Fatalf("backend group should have only s2, got %v", gotInBackend)
	}
	if !reflect.DeepEqual(gotInHot, []string{"s1"}) {
		t.Fatalf("hot folder should have only s1, got %v", gotInHot)
	}
}

// TestBuildRailRows_FoldersAlphabetical: folder headers sort
// alphabetically among themselves, independent of the auto-group
// order.
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

// TestBuildRailRows_EmptyFoldersStateMatchesLegacyShape: a zero
// FoldersState produces the same rail layout as the pre-folders
// implementation. This is the critical backward-compat check — every
// existing user who hasn't created a folder must see the same rail.
func TestBuildRailRows_EmptyFoldersStateMatchesLegacyShape(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
		{ID: "s2", Name: "worker", Tags: []string{"backend"}},
		{ID: "s3", Name: "ui", Tags: []string{"frontend"}},
	}
	rows := BuildRailRows(sessions, sessions, map[string]bool{}, FoldersState{})

	// Legacy expected: header backend, s1, s2, header frontend, s3.
	wantKinds := []RailRowKind{
		RailRowHeader, RailRowSession, RailRowSession,
		RailRowHeader, RailRowSession,
	}
	wantNames := []string{"backend", "api", "worker", "frontend", "ui"}
	gotKinds := make([]RailRowKind, len(rows))
	gotNames := make([]string, len(rows))
	for i, r := range rows {
		gotKinds[i] = r.Kind
		if r.Kind == RailRowSession {
			gotNames[i] = r.Session.Name
		} else {
			gotNames[i] = r.GroupName
		}
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("kinds: got %v want %v", gotKinds, wantKinds)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("names: got %v want %v", gotNames, wantNames)
	}
}
