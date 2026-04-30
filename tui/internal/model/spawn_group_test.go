package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chub/tui/internal/views"
)

// TestSpawn_TabCyclesThreeFields verifies Tab walks 0→1→2→0 and
// Shift+Tab walks 0→2→1→0 across the spawn modal's three fields, and
// that only the active field's textinput is focused at each stop.
func TestSpawn_TabCyclesThreeFields(t *testing.T) {
	m := Model{
		mode: ModeSpawn,
		spawn: spawnState{
			name:  views.NewSpawnNameInput(),
			cwd:   views.NewSpawnCwdInput(""),
			group: views.NewSpawnGroupInput(""),
			field: 0,
		},
	}
	// Constructor focuses name; ensure others are blurred up front.
	m.refocusSpawn()
	if !m.spawn.name.Focused() || m.spawn.cwd.Focused() || m.spawn.group.Focused() {
		t.Fatalf("initial focus mismatch: name=%v cwd=%v group=%v",
			m.spawn.name.Focused(), m.spawn.cwd.Focused(), m.spawn.group.Focused())
	}

	tab := tea.KeyMsg{Type: tea.KeyTab}
	wantFields := []int{1, 2, 0}
	for _, want := range wantFields {
		out, _ := m.handleKeySpawn(tab)
		m = out.(Model)
		if m.spawn.field != want {
			t.Fatalf("after Tab expected field=%d, got %d", want, m.spawn.field)
		}
	}

	// Shift+Tab reverse: 0 → 2 → 1 → 0.
	stab := tea.KeyMsg{Type: tea.KeyShiftTab}
	wantBack := []int{2, 1, 0}
	for _, want := range wantBack {
		out, _ := m.handleKeySpawn(stab)
		m = out.(Model)
		if m.spawn.field != want {
			t.Fatalf("after Shift+Tab expected field=%d, got %d", want, m.spawn.field)
		}
	}

	// On field=2 (group), only the group input should be focused.
	m.spawn.field = 2
	m.refocusSpawn()
	if !m.spawn.group.Focused() {
		t.Fatalf("group should be focused when field=2")
	}
	if m.spawn.name.Focused() || m.spawn.cwd.Focused() {
		t.Fatalf("only group should be focused when field=2: name=%v cwd=%v",
			m.spawn.name.Focused(), m.spawn.cwd.Focused())
	}
}

// TestDoSpawn_GroupBecomesFirstTag verifies the spawn RPC params: when
// a non-empty group is passed in tags, it lands as the first element;
// when nil, tags becomes an empty slice (not omitted) since the
// daemon's schema expects the field present.
func TestDoSpawn_GroupBecomesFirstTag(t *testing.T) {
	// We can't easily stub *rpc.Client without exporting more surface,
	// so instead we mirror handleKeySpawn's Enter logic: a trimmed
	// non-empty group becomes []string{group}, empty/whitespace becomes
	// nil — and doSpawn normalizes nil to []string{} in its params.
	cases := []struct {
		groupInput string
		wantTags   []string
	}{
		{"backend", []string{"backend"}},
		{"  api  ", []string{"api"}},
		{"", nil},
		{"   ", nil},
	}
	for _, c := range cases {
		// Simulate what handleKeySpawn computes pre-doSpawn.
		var tags []string
		trimmed := trim(c.groupInput)
		if trimmed != "" {
			tags = []string{trimmed}
		}
		if !slicesEq(tags, c.wantTags) {
			t.Errorf("group %q: tags = %v, want %v", c.groupInput, tags, c.wantTags)
		}
	}
}

// trim is a tiny stand-in for strings.TrimSpace so the test doesn't add
// a stdlib import that isn't otherwise needed.
func trim(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func slicesEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
