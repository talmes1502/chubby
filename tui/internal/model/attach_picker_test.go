package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chubby/tui/internal/views"
)

// TestAttachPicker_OpensOnCtrlA verifies Ctrl+A in main mode flips the
// model into ModeAttach and emits a non-nil command (the scan
// pipeline). We don't run the cmd here — that requires a live
// rpc.Client; this is a pure reducer test.
func TestAttachPicker_OpensOnCtrlA(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m2 := out.(Model)
	if m2.mode != ModeAttach {
		t.Fatalf("expected ModeAttach after Ctrl+A, got %v", m2.mode)
	}
	if !m2.attach.loading {
		t.Fatalf("expected attach.loading=true while scan in flight")
	}
	if m2.attach.selected == nil {
		t.Fatalf("attach.selected map should be initialized non-nil")
	}
	if cmd == nil {
		t.Fatalf("expected a non-nil tea.Cmd (scan_candidates RPC) after Ctrl+A")
	}
}

// TestAttachPicker_SpaceTogglesSelection prepopulates a small
// candidates slice and asserts that pressing Space (the toggle key)
// flips selected[cursor]. Pressing it again unselects.
func TestAttachPicker_SpaceTogglesSelection(t *testing.T) {
	m := Model{
		mode:           ModeAttach,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
		attach: attachState{
			candidates: []map[string]any{
				{"pid": float64(1111), "cwd": "/a", "classification": "tmux_full"},
				{"pid": float64(2222), "cwd": "/b", "classification": "promote_required"},
			},
			selected: map[int]bool{},
			cursor:   0,
		},
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m2 := out.(Model)
	if !m2.attach.selected[0] {
		t.Fatalf("Space should select the row under cursor (idx 0)")
	}
	// Toggle again — now off.
	out, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m3 := out.(Model)
	if m3.attach.selected[0] {
		t.Fatalf("Space should toggle off on second press")
	}
}

// TestAttachPicker_AAllSkipsAlreadyAttached ensures the bulk-select
// keybind 'a' selects every candidate that isn't flagged
// already_attached. Already-attached rows are skipped because issuing a
// second attach RPC for the same pid would be a no-op (or worse, an
// error) on the daemon side.
func TestAttachPicker_AAllSkipsAlreadyAttached(t *testing.T) {
	m := Model{
		mode:           ModeAttach,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
		attach: attachState{
			candidates: []map[string]any{
				{"pid": float64(1), "classification": "tmux_full", "already_attached": false},
				{"pid": float64(2), "classification": "tmux_full", "already_attached": true},
				{"pid": float64(3), "classification": "promote_required", "already_attached": false},
			},
			selected: map[int]bool{},
		},
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m2 := out.(Model)
	if !m2.attach.selected[0] {
		t.Fatalf("expected idx 0 (not already attached) to be selected")
	}
	if m2.attach.selected[1] {
		t.Fatalf("idx 1 is already_attached — 'a' must skip it")
	}
	if !m2.attach.selected[2] {
		t.Fatalf("expected idx 2 (not already attached) to be selected")
	}
}

// TestAttachPicker_EscReturnsToMain — Esc bails out of the picker
// without firing anything. Mode flips back to ModeMain; pre-existing
// state on the model is left intact (the next Ctrl+A re-initialises
// attachState explicitly).
func TestAttachPicker_EscReturnsToMain(t *testing.T) {
	m := Model{
		mode:           ModeAttach,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
		attach: attachState{
			candidates: []map[string]any{
				{"pid": float64(1), "classification": "tmux_full"},
			},
			selected: map[int]bool{0: true},
		},
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := out.(Model)
	if m2.mode != ModeMain {
		t.Fatalf("expected ModeMain after Esc, got %v", m2.mode)
	}
	if cmd != nil {
		t.Fatalf("Esc should not emit a command, got %v", cmd)
	}
}
