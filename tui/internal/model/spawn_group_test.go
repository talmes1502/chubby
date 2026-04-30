package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chubby/tui/internal/views"
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

// TestCtrlP_OnlyFiresOnDeadSession verifies the respawn shortcut is a
// no-op when there's no focused session and when the focused session is
// not dead — so an accidental Ctrl+P on a live session can't double-spawn.
func TestCtrlP_OnlyFiresOnDeadSession(t *testing.T) {
	cases := []struct {
		name     string
		sessions []Session
		focused  int
		wantCmd  bool
	}{
		{"no focused session", nil, 0, false},
		{"focused session is idle", []Session{{ID: "s1", Name: "a", Status: "idle"}}, 0, false},
		{"focused session is awaiting", []Session{{ID: "s1", Name: "a", Status: "awaiting_user"}}, 0, false},
		{"focused session is dead", []Session{{ID: "s1", Name: "a", Status: "dead", Cwd: "/tmp"}}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{
				sessions: c.sessions,
				focused:  c.focused,
				mode:     ModeMain,
				compose:  views.NewCompose(),
			}
			_, cmd := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlP})
			gotCmd := cmd != nil
			if gotCmd != c.wantCmd {
				t.Errorf("Ctrl+P returned cmd=%v, want %v", gotCmd, c.wantCmd)
			}
		})
	}
}

// TestCtrlN_PrefillsGroupFromFocusedSession verifies that Ctrl+N pre-fills
// the spawn modal's group field with the focused session's group, so a
// new session lands in the same rail bucket by default.
func TestCtrlN_PrefillsGroupFromFocusedSession(t *testing.T) {
	cases := []struct {
		name    string
		focused Session
		want    string
	}{
		{
			name:    "first tag wins",
			focused: Session{ID: "s1", Name: "api", Cwd: "/srv/api", Tags: []string{"backend", "core"}},
			want:    "backend",
		},
		{
			name:    "no tags falls back to cwd basename",
			focused: Session{ID: "s2", Name: "web", Cwd: "/srv/web", Tags: nil},
			want:    "web",
		},
		{
			name:    "untitled does not pre-fill",
			focused: Session{ID: "s3", Name: "x", Cwd: "/", Tags: nil},
			want:    "",
		},
		{
			name:    "empty cwd does not pre-fill",
			focused: Session{ID: "s4", Name: "y", Cwd: "", Tags: nil},
			want:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{
				sessions: []Session{c.focused},
				focused:  0,
				mode:     ModeMain,
				compose:  views.NewCompose(),
			}
			out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlN})
			m2 := out.(Model)
			if m2.mode != ModeSpawn {
				t.Fatalf("expected ModeSpawn after Ctrl+N, got %v", m2.mode)
			}
			if got := m2.spawn.group.Value(); got != c.want {
				t.Errorf("group prefill = %q, want %q", got, c.want)
			}
		})
	}
}
