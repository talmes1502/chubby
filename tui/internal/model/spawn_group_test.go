package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chubby/tui/internal/views"
)

// TestSpawn_EnterAdvancesUntilLastField — Enter on the first three
// fields (name/cwd/branch) advances to the next field (form-fill
// convention); only Enter on the folder field (the last) submits.
// Tab purely completes paths, Enter does the "I'm done with this
// field" gesture.
func TestSpawn_EnterAdvancesUntilLastField(t *testing.T) {
	m := Model{
		mode: ModeSpawn,
		spawn: spawnState{
			name:   views.NewSpawnNameInput(),
			cwd:    views.NewSpawnCwdInput(""),
			branch: views.NewSpawnBranchInput(""),
			folder: views.NewSpawnFolderInput(""),
			field:  0,
		},
	}
	m.refocusSpawn()
	enter := tea.KeyMsg{Type: tea.KeyEnter}

	for want := 1; want <= 3; want++ {
		out, _ := m.handleKeySpawn(enter)
		m = out.(Model)
		if m.spawn.field != want {
			t.Fatalf("Enter on field %d should advance to field %d, got %d",
				want-1, want, m.spawn.field)
		}
	}
}

// TestSpawn_TabCyclesFourFields verifies Tab walks 0→1→2→3→0 and
// Shift+Tab walks 0→3→2→1→0 across the spawn modal's four fields,
// and that only the active field's textinput is focused at each stop.
func TestSpawn_TabCyclesFourFields(t *testing.T) {
	m := Model{
		mode: ModeSpawn,
		spawn: spawnState{
			name:   views.NewSpawnNameInput(),
			cwd:    views.NewSpawnCwdInput(""),
			branch: views.NewSpawnBranchInput(""),
			folder: views.NewSpawnFolderInput(""),
			field:  0,
		},
	}
	// Constructor focuses name; ensure others are blurred up front.
	m.refocusSpawn()
	if !m.spawn.name.Focused() || m.spawn.cwd.Focused() ||
		m.spawn.branch.Focused() || m.spawn.folder.Focused() {
		t.Fatalf("initial focus mismatch: name=%v cwd=%v branch=%v folder=%v",
			m.spawn.name.Focused(), m.spawn.cwd.Focused(),
			m.spawn.branch.Focused(), m.spawn.folder.Focused())
	}

	tab := tea.KeyMsg{Type: tea.KeyTab}
	wantFields := []int{1, 2, 3, 0}
	for _, want := range wantFields {
		out, _ := m.handleKeySpawn(tab)
		m = out.(Model)
		if m.spawn.field != want {
			t.Fatalf("after Tab expected field=%d, got %d", want, m.spawn.field)
		}
	}

	// Shift+Tab reverse: 0 → 3 → 2 → 1 → 0.
	stab := tea.KeyMsg{Type: tea.KeyShiftTab}
	wantBack := []int{3, 2, 1, 0}
	for _, want := range wantBack {
		out, _ := m.handleKeySpawn(stab)
		m = out.(Model)
		if m.spawn.field != want {
			t.Fatalf("after Shift+Tab expected field=%d, got %d", want, m.spawn.field)
		}
	}

	// On field=2 (branch), only the branch input should be focused.
	m.spawn.field = 2
	m.refocusSpawn()
	if !m.spawn.branch.Focused() {
		t.Fatalf("branch should be focused when field=2")
	}
	if m.spawn.name.Focused() || m.spawn.cwd.Focused() || m.spawn.folder.Focused() {
		t.Fatalf("only branch should be focused when field=2")
	}
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

// TestCtrlN_PrefillsFolderFromFocusedSession verifies that Ctrl+N
// pre-fills the spawn modal's folder field with the focused session's
// currently-assigned folder (D10b), so a new session lands in the
// same TUI folder by default. Sessions not in any folder produce an
// empty pre-fill.
func TestCtrlN_PrefillsFolderFromFocusedSession(t *testing.T) {
	folders := FoldersState{Folders: map[string][]string{}}
	folders.Assign("priority", "s1")

	cases := []struct {
		name    string
		focused Session
		want    string
	}{
		{
			name:    "session in folder pre-fills folder name",
			focused: Session{ID: "s1", Name: "api"},
			want:    "priority",
		},
		{
			name:    "session not in any folder leaves field empty",
			focused: Session{ID: "s2", Name: "web", Cwd: "/srv/web"},
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
				folders:  folders,
			}
			out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlN})
			m2 := out.(Model)
			if m2.mode != ModeSpawn {
				t.Fatalf("expected ModeSpawn after Ctrl+N, got %v", m2.mode)
			}
			if got := m2.spawn.folder.Value(); got != c.want {
				t.Errorf("folder prefill = %q, want %q", got, c.want)
			}
		})
	}
}
