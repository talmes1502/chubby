package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// TestCtrlF_OpensNewFolderModal: pressing Ctrl+F from ModeMain flips
// to ModeNewFolder with a focused, empty textinput. (Ctrl+M was the
// original spec; terminals collapse Ctrl+M into Enter, so we picked
// Ctrl+F as a near-equivalent free binding with a sane mnemonic.)
func TestCtrlF_OpensNewFolderModal(t *testing.T) {
	withTempChubbyHome(t)
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		// Pane-aware refactor (v0.1.4): chubby chords (Ctrl+F here)
		// are rail-only; conversation pane forwards to PTY.
		activePane: PaneRail,
	}
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := out.(Model)
	if got.mode != ModeNewFolder {
		t.Fatalf("expected ModeNewFolder, got %v", got.mode)
	}
	if !got.newFolder.input.Focused() {
		t.Fatalf("input should be focused")
	}
	if got.newFolder.input.Value() != "" {
		t.Fatalf("input should be empty, got %q", got.newFolder.input.Value())
	}
}

// TestNewFolder_EnterCreatesFolder: typing a name and hitting Enter
// writes folders.json and returns to ModeMain.
func TestNewFolder_EnterCreatesFolder(t *testing.T) {
	withTempChubbyHome(t)
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		// Pane-aware refactor (v0.1.4): chubby chords (Ctrl+F here)
		// are rail-only; conversation pane forwards to PTY.
		activePane: PaneRail,
	}
	// Open modal.
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = out.(Model)
	// Type the name.
	m.newFolder.input.SetValue("priority")
	// Press Enter.
	out, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := out.(Model)
	if got.mode != ModeMain {
		t.Fatalf("expected ModeMain after create, got %v", got.mode)
	}
	if _, ok := got.folders.Folders["priority"]; !ok {
		t.Fatalf("folder 'priority' should exist in model state, got %v", got.folders.Folders)
	}
	// Round-trip via disk to confirm persistence.
	on := LoadFolders()
	if _, ok := on.Folders["priority"]; !ok {
		t.Fatalf("folder 'priority' should be on disk, got %v", on.Folders)
	}
}

// TestNewFolder_DuplicateRejected: trying to create a folder with the
// same name as an existing folder surfaces an error and keeps the
// modal open.
func TestNewFolder_DuplicateRejected(t *testing.T) {
	withTempChubbyHome(t)
	// Pre-seed an existing folder.
	pre := FoldersState{Folders: map[string][]string{}}
	_ = pre.CreateFolder("priority")
	if err := SaveFolders(pre); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		folders:        LoadFolders(),
		activePane:     PaneRail,
	}
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = out.(Model)
	m.newFolder.input.SetValue("priority")
	out, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := out.(Model)
	if got.mode != ModeNewFolder {
		t.Fatalf("expected to remain in ModeNewFolder on duplicate, got %v", got.mode)
	}
	if got.newFolder.err == nil {
		t.Fatalf("expected an err on duplicate, got nil")
	}
}

// TestNewFolder_EscCancels: Esc returns to ModeMain without writing
// anything.
func TestNewFolder_EscCancels(t *testing.T) {
	withTempChubbyHome(t)
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		// Pane-aware refactor (v0.1.4): chubby chords (Ctrl+F here)
		// are rail-only; conversation pane forwards to PTY.
		activePane: PaneRail,
	}
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = out.(Model)
	m.newFolder.input.SetValue("priority")
	out, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	got := out.(Model)
	if got.mode != ModeMain {
		t.Fatalf("expected ModeMain after esc, got %v", got.mode)
	}
	on := LoadFolders()
	if _, ok := on.Folders["priority"]; ok {
		t.Fatalf("folder should NOT have been persisted on cancel: %v", on.Folders)
	}
}
