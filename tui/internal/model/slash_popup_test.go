package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSlashPopup_VisibleWhenComposeStartsWithSlash(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("/m")
	m.updateSlashPopup()
	if !m.slashPopupVisible() {
		t.Fatalf("popup should be visible for /m")
	}
	found := false
	for _, c := range m.slashPopupCmds {
		if c.Name == "model" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected /model in matches; got %+v", m.slashPopupCmds)
	}
}

func TestSlashPopup_HiddenWhenSpaceTyped(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("/model claude")
	m.updateSlashPopup()
	if m.slashPopupVisible() {
		t.Fatalf("popup should be hidden after space (arg-mode)")
	}
}

func TestSlashPopup_HiddenForNonSlash(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("hello")
	m.updateSlashPopup()
	if m.slashPopupVisible() {
		t.Fatalf("popup should be hidden when value doesn't start with /")
	}
}

func TestSlashPopup_BareSlashShowsAll(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("/")
	m.updateSlashPopup()
	if !m.slashPopupVisible() {
		t.Fatalf("popup should be visible for bare /")
	}
	if len(m.slashPopupCmds) < 2 {
		t.Fatalf("bare / should match many commands; got %d", len(m.slashPopupCmds))
	}
}

func TestSlashPopup_UpDownNavigates(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("/")
	m.updateSlashPopup()
	if len(m.slashPopupCmds) < 2 {
		t.Skip("not enough commands to test navigation")
	}
	m.slashPopupCursor = 0
	nm, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyDown})
	m2 := nm.(Model)
	if m2.slashPopupCursor != 1 {
		t.Fatalf("expected cursor=1 after down, got %d", m2.slashPopupCursor)
	}
	nm2, _ := m2.handleKeyMain(tea.KeyMsg{Type: tea.KeyUp})
	m3 := nm2.(Model)
	if m3.slashPopupCursor != 0 {
		t.Fatalf("expected cursor=0 after up, got %d", m3.slashPopupCursor)
	}
}

func TestSlashPopup_DownClampsAtEnd(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("/")
	m.updateSlashPopup()
	last := len(m.slashPopupCmds) - 1
	m.slashPopupCursor = last
	nm, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyDown})
	m2 := nm.(Model)
	if m2.slashPopupCursor != last {
		t.Fatalf("down at last should stay at %d, got %d", last, m2.slashPopupCursor)
	}
}

func TestSlashPopup_UpClampsAtZero(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("/")
	m.updateSlashPopup()
	m.slashPopupCursor = 0
	nm, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyUp})
	m2 := nm.(Model)
	if m2.slashPopupCursor != 0 {
		t.Fatalf("up at 0 should stay at 0, got %d", m2.slashPopupCursor)
	}
}

func TestSlashPopup_EnterAccepts(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("/m")
	m.updateSlashPopup()
	if len(m.slashPopupCmds) == 0 {
		t.Fatal("no matches for /m")
	}
	chosen := m.slashPopupCmds[0].Name
	nm, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := nm.(Model)
	want := "/" + chosen + " "
	if m2.compose.Value() != want {
		t.Fatalf("compose: got %q want %q", m2.compose.Value(), want)
	}
	if m2.slashPopupVisible() {
		t.Fatalf("popup should hide after accept")
	}
}

func TestSlashPopup_TabAccepts(t *testing.T) {
	// With popup visible, Tab takes the highlighted entry instead of
	// running the legacy Tab-cycle complete.
	m := New(nil)
	m.compose.SetValue("/m")
	m.updateSlashPopup()
	if len(m.slashPopupCmds) == 0 {
		t.Fatal("no matches for /m")
	}
	chosen := m.slashPopupCmds[0].Name
	nm, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyTab})
	m2 := nm.(Model)
	want := "/" + chosen + " "
	if m2.compose.Value() != want {
		t.Fatalf("compose: got %q want %q", m2.compose.Value(), want)
	}
	if m2.slashPopupVisible() {
		t.Fatalf("popup should hide after Tab-accept")
	}
}

func TestSlashPopup_EscDismisses(t *testing.T) {
	m := New(nil)
	m.compose.SetValue("/m")
	m.updateSlashPopup()
	if !m.slashPopupVisible() {
		t.Fatal("expected popup visible before Esc")
	}
	nm, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := nm.(Model)
	if m2.slashPopupVisible() {
		t.Fatalf("popup should hide after Esc")
	}
	// Compose value untouched by Esc — still "/m".
	if m2.compose.Value() != "/m" {
		t.Fatalf("Esc should not mutate compose; got %q", m2.compose.Value())
	}
}

func TestSlashPopup_CursorClampedAfterRefilter(t *testing.T) {
	// Cursor was at the end of a wide match set; user types a more
	// specific prefix that shrinks the set. updateSlashPopup must
	// clamp cursor to a valid index.
	m := New(nil)
	m.compose.SetValue("/")
	m.updateSlashPopup()
	m.slashPopupCursor = len(m.slashPopupCmds) - 1
	m.compose.SetValue("/mo") // probably 1 match (/model)
	m.updateSlashPopup()
	if m.slashPopupCursor >= len(m.slashPopupCmds) {
		t.Fatalf("cursor %d out of bounds for %d matches",
			m.slashPopupCursor, len(m.slashPopupCmds))
	}
}
