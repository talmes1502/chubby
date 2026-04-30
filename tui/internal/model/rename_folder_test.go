package model

import (
	"testing"
)

// TestEnterRename_OnFolder routes to RenameFolder when the rail
// cursor is on a folder header. The sessions slice is empty (no
// per-session retag) and the input pre-fills with the folder name.
func TestEnterRename_OnFolder(t *testing.T) {
	withTempChubbyHome(t)
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("priority", "s1")
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
	}
	m := Model{
		sessions:       sessions,
		groupCollapsed: map[string]bool{},
		mode:           ModeMain,
		folders:        folders,
	}
	// Rail rows: [folder priority, session api(in folder), header
	// backend (untouched because s1 is in folder)] — wait, s1 is in
	// the folder so backend has no sessions to render. So actual
	// rows: [folder priority, session api]. Cursor 0 = folder
	// header.
	m.railCursor = 0
	out, _ := m.enterRenameMode()
	got := out.(Model)
	if got.mode != ModeRename {
		t.Fatalf("expected ModeRename, got %v", got.mode)
	}
	if got.rename.target != RenameFolder {
		t.Fatalf("expected RenameFolder, got %v", got.rename.target)
	}
	if got.rename.oldName != "priority" {
		t.Fatalf("oldName: got %q want priority", got.rename.oldName)
	}
	if len(got.rename.sessions) != 0 {
		t.Fatalf("sessions slice should be empty for folder rename, got %v", got.rename.sessions)
	}
	if got.rename.input.Value() != "priority" {
		t.Fatalf("input pre-fill: got %q want priority", got.rename.input.Value())
	}
}

// TestDoRenameFolder_RewritesFoldersJson: doRenameFolder updates the
// on-disk folders.json from old name to new name, preserving member
// session ids.
func TestDoRenameFolder_RewritesFoldersJson(t *testing.T) {
	withTempChubbyHome(t)
	pre := FoldersState{Folders: map[string][]string{}}
	pre.Assign("priority", "s1")
	pre.Assign("priority", "s2")
	if err := SaveFolders(pre); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m := Model{}
	cmd := m.doRenameFolder("priority", "hot")
	msg := cmd()
	if _, ok := msg.(renameDoneMsg); !ok {
		t.Fatalf("expected renameDoneMsg, got %T (%v)", msg, msg)
	}
	on := LoadFolders()
	if _, ok := on.Folders["priority"]; ok {
		t.Fatalf("old folder name should be gone: %v", on.Folders)
	}
	got := on.SessionsInFolder("hot")
	if len(got) != 2 {
		t.Fatalf("renamed folder should keep session ids, got %v", got)
	}
}

// TestDoRenameFolder_CollisionIsRecoverable: renaming onto an
// existing folder yields renameFolderFailedMsg, not renameDoneMsg.
func TestDoRenameFolder_CollisionIsRecoverable(t *testing.T) {
	withTempChubbyHome(t)
	pre := FoldersState{Folders: map[string][]string{}}
	pre.Assign("priority", "s1")
	pre.Assign("hot", "s2")
	if err := SaveFolders(pre); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m := Model{}
	cmd := m.doRenameFolder("priority", "hot")
	msg := cmd()
	if _, ok := msg.(renameFolderFailedMsg); !ok {
		t.Fatalf("expected renameFolderFailedMsg, got %T (%v)", msg, msg)
	}
	// On-disk state should be unchanged.
	on := LoadFolders()
	if got := on.FolderForSession("s1"); got != "priority" {
		t.Fatalf("s1 should still be in priority, got %q", got)
	}
}
