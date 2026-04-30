package model

import (
	"testing"

	"github.com/USER/chub/tui/internal/views"
)

// TestAutoOpenSpawnModal_OnFirstEmptyList verifies that the very first
// listMsg with zero sessions auto-flips the model into ModeSpawn so the
// user doesn't stare at an empty viewport on a fresh `chub up`.
func TestAutoOpenSpawnModal_OnFirstEmptyList(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	out, _ := m.Update(listMsg(nil))
	m2 := out.(Model)
	if m2.mode != ModeSpawn {
		t.Fatalf("expected ModeSpawn after first empty listMsg, got %v", m2.mode)
	}
	if !m2.initialListReceived {
		t.Fatalf("initialListReceived should be true after first listMsg")
	}
}

// TestAutoOpenSpawnModal_NotReopenedOnSecondEmpty verifies the auto-open
// fires once: a second empty listMsg (e.g. after the user closes the
// modal with Esc) must not re-open it.
func TestAutoOpenSpawnModal_NotReopenedOnSecondEmpty(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	out, _ := m.Update(listMsg(nil))
	m = out.(Model)
	if m.mode != ModeSpawn {
		t.Fatalf("precondition: expected ModeSpawn after first empty listMsg")
	}
	// Simulate the user dismissing the modal.
	m.mode = ModeMain
	out, _ = m.Update(listMsg(nil))
	m2 := out.(Model)
	if m2.mode != ModeMain {
		t.Fatalf("expected ModeMain after second empty listMsg, got %v (modal should not re-open)", m2.mode)
	}
}

// TestAutoOpenSpawnModal_NotOpenedWhenSessionsExist verifies the
// auto-open does NOT fire when the first list contains sessions —
// the user has work to focus on, no need for a modal in their face.
func TestAutoOpenSpawnModal_NotOpenedWhenSessionsExist(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	out, _ := m.Update(listMsg([]Session{
		{ID: "s1", Name: "alpha", Cwd: "/tmp", Status: "idle"},
	}))
	m2 := out.(Model)
	if m2.mode != ModeMain {
		t.Fatalf("expected ModeMain when first list has sessions, got %v", m2.mode)
	}
	if !m2.initialListReceived {
		t.Fatalf("initialListReceived should be true after any listMsg")
	}
}
