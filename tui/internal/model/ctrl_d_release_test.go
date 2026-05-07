package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// First Ctrl+D should NOT release the session — it sets a confirm
// flag and toasts. Only the second press within the window fires
// release_session. Without two-tap protection a stray Ctrl+D
// (literally one keystroke) would nuke a real session.
func TestCtrlD_FirstPressJustToasts(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client:         cl,
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
		sessions:       []Session{{ID: "s1", Name: "api", Status: StatusIdle, Kind: KindWrapped}},
		focused:        0,
		activePane:     PaneRail,
	}
	out, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlD})
	mm := out.(Model)
	if cmd != nil {
		t.Fatalf("first Ctrl+D should not dispatch an RPC; got %T", cmd())
	}
	// pendingDeleteID is now namespaced ("session:" / "folder:")
	// so a folder pending doesn't collide with a session pending of
	// the same id.
	if mm.pendingDeleteID != "session:s1" {
		t.Fatalf("first Ctrl+D should arm the confirm; pendingDeleteID = %q", mm.pendingDeleteID)
	}
	if len(mm.toasts) == 0 {
		t.Fatalf("first Ctrl+D should add a confirm toast")
	}
}

// Second Ctrl+D within the window dispatches release_session.
func TestCtrlD_SecondPressReleases(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:         cl,
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
		sessions:       []Session{{ID: "s1", Name: "api", Status: StatusIdle, Kind: KindWrapped}},
		focused:        0,
		activePane:     PaneRail,
	}
	// Arm the confirm.
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = out.(Model)
	// Second press → dispatch.
	out, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Fatalf("second Ctrl+D should dispatch release_session")
	}
	mm := out.(Model)
	if mm.pendingDeleteID != "" {
		t.Fatalf("pendingDeleteID should clear after dispatch; got %q", mm.pendingDeleteID)
	}
	_ = cmd()
	d.waitForCall(t)
	method, params := d.lastCall()
	if method != "release_session" || params["id"] != "s1" {
		t.Fatalf("expected release_session(s1); got %s(%v)", method, params)
	}
}

// A second Ctrl+D after the window has elapsed must re-arm rather
// than fire — old confirms shouldn't survive the user drifting
// away to typing for a minute and coming back.
func TestCtrlD_StaleConfirmReArms(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client:          cl,
		mode:            ModeMain,
		compose:         views.NewCompose(),
		groupCollapsed:  map[string]bool{},
		scrollOffset:    map[string]int{},
		newSinceScroll:  map[string]int{},
		sessions:        []Session{{ID: "s1", Name: "api", Status: StatusIdle, Kind: KindWrapped}},
		focused:         0,
		pendingDeleteID: "session:s1",
		// Pretend the first press was way before the window.
		pendingDeleteAt: time.Now().Add(-time.Minute),
		activePane:      PaneRail,
	}
	out, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd != nil {
		t.Fatalf("Ctrl+D after a stale confirm should re-arm, not release")
	}
	mm := out.(Model)
	if mm.pendingDeleteID != "session:s1" || time.Since(mm.pendingDeleteAt) > time.Second {
		t.Fatalf("stale confirm should re-arm with a fresh timestamp")
	}
}

// TestCtrlD_DeletesFolderTwoTap: Ctrl+D twice on a folder header
// row deletes the folder. Sessions inside become unfiled — the
// session rows themselves are untouched.
func TestCtrlD_DeletesFolderTwoTap(t *testing.T) {
	withTempChubbyHome(t)
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("priority", "s1")
	if err := SaveFolders(folders); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api", Status: StatusIdle, Kind: KindWrapped}},
		folders:        LoadFolders(),
		focused:        0,
		railCursor:     0, // folder header is row 0 when one folder + one session
		activePane:     PaneRail,
	}
	// Sanity that cursor lands on the folder header.
	rows := m.railRows()
	if len(rows) == 0 || rows[m.railCursor].Kind != RailRowFolder {
		t.Fatalf("test setup: cursor should be on a folder header, got %v", rows[m.railCursor].Kind)
	}
	// First press: arms only.
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = out.(Model)
	if m.pendingDeleteID != "folder:priority" {
		t.Fatalf("first Ctrl+D should arm folder confirm; got %q", m.pendingDeleteID)
	}
	// Second press: dispatches delete.
	out, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Fatalf("second Ctrl+D should dispatch folder delete")
	}
	// Run the cmd and feed the result back.
	msg := cmd()
	out2, _ := out.(Model).Update(msg)
	mm := out2.(Model)
	if _, exists := mm.folders.Folders["priority"]; exists {
		t.Fatalf("folder should be gone from in-memory state; got %v", mm.folders.Folders)
	}
	on := LoadFolders()
	if _, exists := on.Folders["priority"]; exists {
		t.Fatalf("folder should be gone from disk; got %v", on.Folders)
	}
}
