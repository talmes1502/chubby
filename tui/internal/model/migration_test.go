package model

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestMigrate_AssignsByFirstTag: a session with a non-empty first tag
// lands in a folder of the same name, regardless of cwd.
func TestMigrate_AssignsByFirstTag(t *testing.T) {
	withTempChubbyHome(t)
	state := FoldersState{Folders: map[string][]string{}}
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}, Cwd: "/srv/api"},
	}
	n := MigrateAutoGroupingToFolders(sessions, &state)
	if n != 1 {
		t.Fatalf("expected 1 migrated, got %d", n)
	}
	if got := state.FolderForSession("s1"); got != "backend" {
		t.Fatalf("s1 should be in 'backend', got %q", got)
	}
}

// TestMigrate_FallsBackToCwdBasename: a session with no tags is
// assigned by the basename of its cwd.
func TestMigrate_FallsBackToCwdBasename(t *testing.T) {
	withTempChubbyHome(t)
	state := FoldersState{Folders: map[string][]string{}}
	sessions := []Session{
		{ID: "s1", Name: "ui", Cwd: "/Users/me/code/portfolio"},
	}
	n := MigrateAutoGroupingToFolders(sessions, &state)
	if n != 1 {
		t.Fatalf("expected 1 migrated, got %d", n)
	}
	if got := state.FolderForSession("s1"); got != "portfolio" {
		t.Fatalf("s1 should be in 'portfolio', got %q", got)
	}
}

// TestMigrate_SkipsAlreadyAssigned: a session already in a folder
// keeps its existing assignment, even if its tag/cwd would have
// produced a different folder.
func TestMigrate_SkipsAlreadyAssigned(t *testing.T) {
	withTempChubbyHome(t)
	state := FoldersState{Folders: map[string][]string{}}
	state.Assign("priority", "s1")
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}, Cwd: "/srv/api"},
	}
	n := MigrateAutoGroupingToFolders(sessions, &state)
	if n != 0 {
		t.Fatalf("expected 0 migrated (already assigned), got %d", n)
	}
	if got := state.FolderForSession("s1"); got != "priority" {
		t.Fatalf("s1 should remain in 'priority', got %q", got)
	}
	// Make sure no stray "backend" folder was created.
	if _, ok := state.Folders["backend"]; ok {
		t.Fatalf("unexpected 'backend' folder created: %v", state.Folders)
	}
}

// TestMigrate_SkipsUntitled: sessions whose old auto-group key would
// have been "(untitled)" (empty cwd, "/" cwd, no tags) stay unfiled.
func TestMigrate_SkipsUntitled(t *testing.T) {
	withTempChubbyHome(t)
	state := FoldersState{Folders: map[string][]string{}}
	sessions := []Session{
		{ID: "s1", Name: "x", Cwd: ""},
		{ID: "s2", Name: "y", Cwd: "/"},
	}
	n := MigrateAutoGroupingToFolders(sessions, &state)
	if n != 0 {
		t.Fatalf("expected 0 migrated for untitled sessions, got %d", n)
	}
	if got := state.FolderForSession("s1"); got != "" {
		t.Fatalf("s1 should be unfiled, got %q", got)
	}
	if got := state.FolderForSession("s2"); got != "" {
		t.Fatalf("s2 should be unfiled, got %q", got)
	}
}

// TestMigrate_IsIdempotent: a second run is a no-op once the sentinel
// is in place — subsequent sessions added (even with auto-group keys)
// don't get auto-migrated, so the user's manual layout is respected.
func TestMigrate_IsIdempotent(t *testing.T) {
	withTempChubbyHome(t)
	state := FoldersState{Folders: map[string][]string{}}
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
	}
	if n := MigrateAutoGroupingToFolders(sessions, &state); n != 1 {
		t.Fatalf("first run should migrate 1, got %d", n)
	}
	// Second run with a NEW session that would otherwise auto-migrate
	// — the sentinel must short-circuit it.
	sessions = append(sessions, Session{ID: "s2", Name: "ui", Tags: []string{"frontend"}})
	if n := MigrateAutoGroupingToFolders(sessions, &state); n != 0 {
		t.Fatalf("second run should be a no-op (sentinel present), got %d", n)
	}
	if got := state.FolderForSession("s2"); got != "" {
		t.Fatalf("s2 should NOT have been migrated on the second run, got %q", got)
	}
}

// TestMigrate_TouchesSentinel: after a successful run, the sentinel
// file exists at <chubbyDataDir>/folders-migrated.
func TestMigrate_TouchesSentinel(t *testing.T) {
	withTempChubbyHome(t)
	state := FoldersState{Folders: map[string][]string{}}
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
	}
	_ = MigrateAutoGroupingToFolders(sessions, &state)
	sentinel := filepath.Join(chubbyDataDir(), migrationSentinelName)
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected sentinel %q to exist, stat err: %v", sentinel, err)
	}
}

// TestMigrate_TouchesSentinelEvenOnZeroMigrated: a first launch where
// every session is already (untitled) or already-assigned still drops
// the sentinel, so the migration doesn't keep retrying on every
// startup.
func TestMigrate_TouchesSentinelEvenOnZeroMigrated(t *testing.T) {
	withTempChubbyHome(t)
	state := FoldersState{Folders: map[string][]string{}}
	sessions := []Session{
		{ID: "s1", Name: "x", Cwd: "/"}, // would map to (untitled) — skipped
	}
	if n := MigrateAutoGroupingToFolders(sessions, &state); n != 0 {
		t.Fatalf("expected 0 migrated, got %d", n)
	}
	sentinel := filepath.Join(chubbyDataDir(), migrationSentinelName)
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected sentinel after zero-migration run, stat err: %v", sentinel)
	}
}

// TestMigrate_PersistsToDisk: a successful migration writes the new
// assignments to folders.json so the next TUI launch picks them up
// even if the in-memory state were thrown away.
func TestMigrate_PersistsToDisk(t *testing.T) {
	withTempChubbyHome(t)
	state := FoldersState{Folders: map[string][]string{}}
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
		{ID: "s2", Name: "worker", Tags: []string{"backend"}},
		{ID: "s3", Name: "ui", Cwd: "/Users/me/code/web"},
	}
	if n := MigrateAutoGroupingToFolders(sessions, &state); n != 3 {
		t.Fatalf("expected 3 migrated, got %d", n)
	}
	// Reload from disk and verify the same assignments.
	on := LoadFolders()
	gotBackend := append([]string{}, on.SessionsInFolder("backend")...)
	sort.Strings(gotBackend)
	if !reflect.DeepEqual(gotBackend, []string{"s1", "s2"}) {
		t.Fatalf("on-disk 'backend' folder: got %v want [s1 s2]", gotBackend)
	}
	if got := on.SessionsInFolder("web"); !reflect.DeepEqual(got, []string{"s3"}) {
		t.Fatalf("on-disk 'web' folder: got %v want [s3]", got)
	}
}
