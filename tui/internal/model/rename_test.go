package model

import (
	"reflect"
	"sort"
	"testing"
)

// TestEnterRename_OnSession opens ModeRename with target=RenameSession,
// the session id in m.rename.sessions, and the input pre-populated with
// the session's name.
func TestEnterRename_OnSession(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
		{ID: "s2", Name: "worker", Tags: []string{"backend"}},
	}
	m := Model{
		sessions:       sessions,
		groupCollapsed: map[string]bool{},
		mode:           ModeMain,
	}
	// Rail rows: [header backend, session api, session worker].
	// Cursor on row 1 = "api".
	m.railCursor = 1
	out, _ := m.enterRenameMode()
	got := out.(Model)
	if got.mode != ModeRename {
		t.Fatalf("expected ModeRename, got %v", got.mode)
	}
	if got.rename.target != RenameSession {
		t.Fatalf("expected RenameSession, got %v", got.rename.target)
	}
	if got.rename.oldName != "api" {
		t.Fatalf("oldName: got %q want %q", got.rename.oldName, "api")
	}
	if !reflect.DeepEqual(got.rename.sessions, []string{"s1"}) {
		t.Fatalf("sessions: got %v want [s1]", got.rename.sessions)
	}
	if got.rename.input.Value() != "api" {
		t.Fatalf("input pre-fill: got %q want %q", got.rename.input.Value(), "api")
	}
	if !got.rename.input.Focused() {
		t.Fatalf("rename input should be focused")
	}
}

// TestEnterRename_OnGroup opens ModeRename with target=RenameGroup,
// every session id in the group in m.rename.sessions, oldName=group
// label, and the input pre-populated with the group label.
func TestEnterRename_OnGroup(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "api", Tags: []string{"backend"}},
		{ID: "s2", Name: "worker", Tags: []string{"backend"}},
		{ID: "s3", Name: "ui", Tags: []string{"frontend"}},
	}
	m := Model{
		sessions:       sessions,
		groupCollapsed: map[string]bool{},
		mode:           ModeMain,
	}
	// Rail rows: backend header, api, worker, frontend header, ui.
	// Cursor on row 0 = backend header.
	m.railCursor = 0
	out, _ := m.enterRenameMode()
	got := out.(Model)
	if got.mode != ModeRename {
		t.Fatalf("expected ModeRename, got %v", got.mode)
	}
	if got.rename.target != RenameGroup {
		t.Fatalf("expected RenameGroup, got %v", got.rename.target)
	}
	if got.rename.oldName != "backend" {
		t.Fatalf("oldName: got %q want %q", got.rename.oldName, "backend")
	}
	gotIDs := append([]string{}, got.rename.sessions...)
	sort.Strings(gotIDs)
	if !reflect.DeepEqual(gotIDs, []string{"s1", "s2"}) {
		t.Fatalf("sessions: got %v want [s1 s2]", gotIDs)
	}
	if got.rename.input.Value() != "backend" {
		t.Fatalf("input pre-fill: got %q want %q", got.rename.input.Value(), "backend")
	}
}

// TestEnterRename_EmptyRail does nothing when there are no rail rows.
func TestEnterRename_EmptyRail(t *testing.T) {
	m := Model{
		sessions:       nil,
		groupCollapsed: map[string]bool{},
		mode:           ModeMain,
		railCursor:     0,
	}
	out, _ := m.enterRenameMode()
	got := out.(Model)
	if got.mode != ModeMain {
		t.Fatalf("empty rail should leave mode untouched, got %v", got.mode)
	}
}
