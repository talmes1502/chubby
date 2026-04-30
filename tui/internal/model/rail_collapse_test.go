package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chub/tui/internal/views"
)

// withTempHome redirects $HOME to a fresh temp dir for the duration of
// the test so the persistence layer (~/.claude/hub/tui-state.json)
// reads/writes inside the sandbox. The previous HOME is restored on
// cleanup.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, hadPrev := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", dir); err != nil {
		t.Fatalf("setenv HOME: %v", err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("HOME", prev)
		} else {
			_ = os.Unsetenv("HOME")
		}
	})
	return dir
}

// readPersistedRailCollapsed pokes the on-disk JSON directly (rather
// than going through LoadRailCollapsed) so the test asserts the actual
// serialized shape, not just the loader's interpretation of it.
func readPersistedRailCollapsed(t *testing.T, home string) (rail bool, present bool) {
	t.Helper()
	p := filepath.Join(home, ".claude", "hub", "tui-state.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read tui-state.json: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := raw["rail_collapsed"]
	if !ok {
		return false, false
	}
	b, _ := v.(bool)
	return b, true
}

// TestCtrlJ_TogglesRailCollapsed verifies Ctrl+J flips m.railCollapsed
// and persists the new state to tui-state.json.
func TestCtrlJ_TogglesRailCollapsed(t *testing.T) {
	home := withTempHome(t)

	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
	}
	if m.railCollapsed {
		t.Fatalf("precondition: railCollapsed should start false")
	}

	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = out.(Model)
	if !m.railCollapsed {
		t.Fatalf("expected railCollapsed=true after Ctrl+J, got false")
	}
	rail, present := readPersistedRailCollapsed(t, home)
	if !present {
		t.Fatalf("rail_collapsed key missing from tui-state.json after toggle")
	}
	if !rail {
		t.Fatalf("persisted rail_collapsed = false, want true")
	}

	// Second Ctrl+J un-collapses.
	out, _ = m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = out.(Model)
	if m.railCollapsed {
		t.Fatalf("expected railCollapsed=false after second Ctrl+J, got true")
	}
	rail, present = readPersistedRailCollapsed(t, home)
	if !present {
		t.Fatalf("rail_collapsed key missing from tui-state.json after second toggle")
	}
	if rail {
		t.Fatalf("persisted rail_collapsed = true after second toggle, want false")
	}
}

// TestLoadRailCollapsed_MissingFieldDefaultsFalse verifies the migration
// path: a tui-state.json that predates the rail_collapsed key parses
// cleanly and reports false (no surprise auto-collapse on first launch
// after upgrade).
func TestLoadRailCollapsed_MissingFieldDefaultsFalse(t *testing.T) {
	home := withTempHome(t)
	p := filepath.Join(home, ".claude", "hub", "tui-state.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Old shape — only groups_collapsed, no rail_collapsed.
	if err := os.WriteFile(p, []byte(`{"groups_collapsed":["foo"]}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := LoadRailCollapsed(); got != false {
		t.Fatalf("LoadRailCollapsed on legacy file = %v, want false", got)
	}
	// And groups still load too.
	if !LoadCollapsedGroups()["foo"] {
		t.Fatalf("legacy groups should still load")
	}
}

// TestSaveCollapsedGroups_PreservesRailFlag verifies that the
// groups-only save path doesn't accidentally clobber rail_collapsed
// when it was previously true. This guards against a regression where
// SaveCollapsedGroups overwrote the JSON with only its own field.
func TestSaveCollapsedGroups_PreservesRailFlag(t *testing.T) {
	withTempHome(t)
	// First, persist rail_collapsed=true via the full-state path.
	if err := SaveTUIState(TUIState{RailCollapsed: true}); err != nil {
		t.Fatalf("SaveTUIState: %v", err)
	}
	// Then save groups via the legacy path.
	if err := SaveCollapsedGroups(map[string]bool{"alpha": true}); err != nil {
		t.Fatalf("SaveCollapsedGroups: %v", err)
	}
	if !LoadRailCollapsed() {
		t.Fatalf("rail_collapsed was clobbered by SaveCollapsedGroups")
	}
	if !LoadCollapsedGroups()["alpha"] {
		t.Fatalf("alpha group not persisted")
	}
}
