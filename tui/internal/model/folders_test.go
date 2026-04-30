package model

import (
	"os"
	"reflect"
	"testing"
)

// withTempChubbyHome points CHUBBY_HOME at a fresh temp dir so
// LoadFolders/SaveFolders touch isolated state. Restored on cleanup.
func withTempChubbyHome(t *testing.T) {
	t.Helper()
	prev, hadPrev := os.LookupEnv("CHUBBY_HOME")
	dir := t.TempDir()
	if err := os.Setenv("CHUBBY_HOME", dir); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("CHUBBY_HOME", prev)
		} else {
			_ = os.Unsetenv("CHUBBY_HOME")
		}
	})
}

func TestFoldersState_AssignNewFolder(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	s.Assign("backend", "s1")
	if got := s.FolderForSession("s1"); got != "backend" {
		t.Fatalf("FolderForSession: got %q want backend", got)
	}
	if !reflect.DeepEqual(s.SessionsInFolder("backend"), []string{"s1"}) {
		t.Fatalf("SessionsInFolder: %v", s.SessionsInFolder("backend"))
	}
}

func TestFoldersState_AssignMovesAcrossFolders(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	s.Assign("backend", "s1")
	s.Assign("frontend", "s1")
	if got := s.FolderForSession("s1"); got != "frontend" {
		t.Fatalf("FolderForSession after move: got %q want frontend", got)
	}
	if len(s.SessionsInFolder("backend")) != 0 {
		t.Fatalf("backend should be empty, got %v", s.SessionsInFolder("backend"))
	}
}

func TestFoldersState_Unassign(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	s.Assign("backend", "s1")
	s.Assign("backend", "s2")
	s.Unassign("s1")
	if got := s.FolderForSession("s1"); got != "" {
		t.Fatalf("FolderForSession after unassign: got %q want ''", got)
	}
	if !reflect.DeepEqual(s.SessionsInFolder("backend"), []string{"s2"}) {
		t.Fatalf("SessionsInFolder: %v", s.SessionsInFolder("backend"))
	}
}

func TestFoldersState_RenameFolder(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	s.Assign("backend", "s1")
	if err := s.RenameFolder("backend", "infra"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := s.FolderForSession("s1"); got != "infra" {
		t.Fatalf("FolderForSession after rename: got %q want infra", got)
	}
	if _, ok := s.Folders["backend"]; ok {
		t.Fatalf("old folder name still present: %v", s.Folders)
	}
}

func TestFoldersState_RenameFolderCollision(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	s.Assign("backend", "s1")
	s.Assign("frontend", "s2")
	if err := s.RenameFolder("backend", "frontend"); err == nil {
		t.Fatalf("expected collision error, got nil")
	}
}

func TestFoldersState_RenameFolderUnknown(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	if err := s.RenameFolder("ghost", "infra"); err == nil {
		t.Fatalf("expected error for unknown folder, got nil")
	}
}

func TestFoldersState_CreateFolderDuplicate(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	if err := s.CreateFolder("backend"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := s.CreateFolder("backend"); err == nil {
		t.Fatalf("second create should error")
	}
}

func TestFoldersState_AllFolderNamesAlphabetical(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	_ = s.CreateFolder("Zeta")
	_ = s.CreateFolder("alpha")
	_ = s.CreateFolder("Mu")
	got := s.AllFolderNames()
	want := []string{"alpha", "Mu", "Zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllFolderNames: got %v want %v", got, want)
	}
}

func TestSaveAndLoadFolders_Roundtrip(t *testing.T) {
	withTempChubbyHome(t)
	in := FoldersState{Folders: map[string][]string{}}
	in.Assign("backend", "s1")
	in.Assign("backend", "s2")
	in.Assign("frontend", "s3")
	if err := SaveFolders(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out := LoadFolders()
	if !reflect.DeepEqual(out.SessionsInFolder("backend"), []string{"s1", "s2"}) {
		t.Fatalf("backend ids: %v", out.SessionsInFolder("backend"))
	}
	if !reflect.DeepEqual(out.SessionsInFolder("frontend"), []string{"s3"}) {
		t.Fatalf("frontend ids: %v", out.SessionsInFolder("frontend"))
	}
}

func TestLoadFolders_MissingFileGivesEmpty(t *testing.T) {
	withTempChubbyHome(t)
	out := LoadFolders()
	if len(out.Folders) != 0 {
		t.Fatalf("expected empty, got %v", out.Folders)
	}
	// Map must be initialized so callers can write to it.
	if out.Folders == nil {
		t.Fatalf("Folders map should be non-nil")
	}
}

func TestLoadFolders_StripsEmptyKeyOnLoad(t *testing.T) {
	withTempChubbyHome(t)
	// Hand-craft a folders.json with an empty-string key to ensure
	// LoadFolders sanitizes it (the empty key is reserved for the
	// implicit "(unassigned)" bucket and must never enter the map).
	dir := chubbyDataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(foldersPath(),
		[]byte(`{"folders":{"":["s1"],"backend":["s2"]}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := LoadFolders()
	if _, ok := out.Folders[""]; ok {
		t.Fatalf("empty key should be stripped: %v", out.Folders)
	}
	if !reflect.DeepEqual(out.SessionsInFolder("backend"), []string{"s2"}) {
		t.Fatalf("backend should survive: %v", out.Folders)
	}
}

func TestFoldersState_AssignEmptyFolderIsNoop(t *testing.T) {
	s := FoldersState{Folders: map[string][]string{}}
	s.Assign("", "s1")
	if len(s.Folders) != 0 {
		t.Fatalf("Assign with empty folder must not create entries: %v", s.Folders)
	}
}
