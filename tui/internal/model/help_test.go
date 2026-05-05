package model

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// Help text must mention every Phase 1-7 feature so the user
// discovers it via "?". The test is intentionally lightweight — a
// substring match per feature keyword. If we rename a key chord
// later (e.g. Ctrl+P → Cmd+K), the corresponding help line will
// need updating; this test fails fast with a useful error.
func TestHelpBody_DocumentsEveryShippedFeature(t *testing.T) {
	required := []string{
		// Phase 1 — worktrees
		"--branch", "worktrees", "--pr",
		// Phase 2 — lifecycle scripts
		".chubby/config.json", "setup", "teardown", "config.local.json",
		// Phase 3 — branch ahead/behind
		"↑N", "↓N", "ahead",
		// Phase 4 — agent-context CLI
		"--json", "--quiet", "CLAUDE_CODE", "CHUBBY_AGENT", "CI",
		// Phase 5 — port detection
		"🌐", ":3000",
		// Phase 6 — presets
		"chubby preset", "{date}",
		// Phase 7 — quick switcher + env hardening
		"Ctrl+P", "fuzzy", "TERM_PROGRAM", "FORCE_HYPERLINK",
		// Editor
		"$CHUBBY_EDITOR", "Ctrl+X",
		// Phase 8 — moltty polish
		":clone", ":restart",
		"Shift+H", "cross-project history",
	}
	for _, kw := range required {
		if !strings.Contains(helpBody, kw) {
			t.Errorf("help body missing keyword %q — did you ship a feature without documenting it?", kw)
		}
	}
}

// The help renderer windows lines based on terminal height so the
// content can grow past the screen. Verify the visible slice changes
// when we scroll.
func TestViewHelp_ScrollsVisibleWindow(t *testing.T) {
	m := Model{
		mode:           ModeHelp,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
		width:          80,
		height:         20,
		helpScroll:     0,
	}
	top := m.viewHelp()
	m.helpScroll = 30
	mid := m.viewHelp()
	if top == mid {
		t.Fatalf("scrolling should change the rendered window")
	}
	// The header line "chubby-tui keys" is at line 0; should be
	// visible at scroll=0 but not at scroll=30.
	if !strings.Contains(top, "chubby-tui keys") {
		t.Fatalf("scroll=0 should show the header; got %q", top)
	}
}

// "?" is the open shortcut and also closes the overlay; bind it on
// both sides so the user can press "?" to toggle.
func TestHelp_QuestionMarkClosesOverlay(t *testing.T) {
	m := Model{
		mode:           ModeHelp,
		compose:        views.NewCompose(),
		sessions:       []Session{},
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyHelp(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'?'},
	})
	if out.(Model).mode != ModeMain {
		t.Fatalf("'?' in ModeHelp should close to ModeMain; got %v", out.(Model).mode)
	}
}

func TestHelp_EscapeClosesOverlay(t *testing.T) {
	m := Model{
		mode:           ModeHelp,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyHelp(tea.KeyMsg{Type: tea.KeyEsc})
	if out.(Model).mode != ModeMain {
		t.Fatalf("Esc should close help; got %v", out.(Model).mode)
	}
}

func TestHelp_DownArrowScrolls(t *testing.T) {
	m := Model{
		mode:           ModeHelp,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyHelp(tea.KeyMsg{Type: tea.KeyDown})
	if out.(Model).helpScroll != 1 {
		t.Fatalf("Down → helpScroll=1; got %d", out.(Model).helpScroll)
	}
	out, _ = out.(Model).handleKeyHelp(tea.KeyMsg{Type: tea.KeyPgDown})
	if out.(Model).helpScroll != 11 {
		t.Fatalf("PgDn → +10; got %d", out.(Model).helpScroll)
	}
}

func TestHelp_UpArrowDoesntGoNegative(t *testing.T) {
	m := Model{
		mode:           ModeHelp,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
		helpScroll:     0,
	}
	out, _ := m.handleKeyHelp(tea.KeyMsg{Type: tea.KeyUp})
	if out.(Model).helpScroll < 0 {
		t.Fatalf("scroll must clamp at 0; got %d", out.(Model).helpScroll)
	}
}

func TestHelp_FooterShownWhenContentExceedsHeight(t *testing.T) {
	m := Model{
		mode:           ModeHelp,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
		width:          80,
		// Forced tiny height so the footer ("line N/M") must show.
		height: 12,
	}
	out := m.viewHelp()
	if !strings.Contains(out, "/") {
		t.Fatalf("scroll-position footer should render when content > height")
	}
}
