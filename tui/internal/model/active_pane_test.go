package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// TestF6_TogglesActivePane: F6 (the new pane-switch key — Tab no
// longer eats the keystroke) toggles activePane between PaneRail
// and PaneConversation.
func TestF6_TogglesActivePane(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api"}},
		focused:        0,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	if m.activePane != PaneConversation {
		t.Fatalf("default activePane should be PaneConversation (typing → claude); got %v", m.activePane)
	}
	f6 := tea.KeyMsg{Type: tea.KeyF6}
	out, _ := m.handleKeyMain(f6)
	m = out.(Model)
	if m.activePane != PaneRail {
		t.Fatalf("after first F6 expected PaneRail, got %v", m.activePane)
	}
	out, _ = m.handleKeyMain(f6)
	m = out.(Model)
	if m.activePane != PaneConversation {
		t.Fatalf("after second F6 expected PaneConversation, got %v", m.activePane)
	}
}

// TestTab_InRailConsumedByAutocompleteNotPaneSwitch: in the rail
// pane, Tab tries slash-command autocompletion before falling
// through to the PTY. With "/m" in compose it should complete (e.g.
// to "/model ") and emit no pane-switch side-effect. After the
// pane-aware refactor, F6 (not Tab) toggles panes — this test just
// confirms the slash-autocomplete branch still wins over PTY
// fallthrough in the rail handler.
func TestTab_InRailConsumedByAutocompleteNotPaneSwitch(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api"}},
		focused:        0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	// Type a slash so trySlashComplete returns ok.
	m.compose.SetValue("/m")
	before := m.compose.Value()
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyTab})
	m = out.(Model)
	if m.activePane != PaneRail {
		t.Fatalf("Tab in rail must not switch pane (F6 owns that); got %v", m.activePane)
	}
	if m.compose.Value() == before {
		t.Fatalf("Tab in rail with slash partial should autocomplete compose; stayed at %q",
			m.compose.Value())
	}
}

// TestArrowsInRailWalkRailCursor: with PaneRail active and compose
// empty, Up/Down move m.railCursor (which in turn updates m.focused
// when landing on a session row).
func TestArrowsInRailWalkRailCursor(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "alpha"},
		{ID: "s2", Name: "beta"},
		{ID: "s3", Name: "gamma"},
	}
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       sessions,
		focused:        0,
		railCursor:     0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	// Down moves cursor to row 1.
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	if m.railCursor != 1 {
		t.Fatalf("after Down expected railCursor=1, got %d", m.railCursor)
	}
	if m.focused != 1 {
		t.Fatalf("after Down expected focused=1, got %d", m.focused)
	}
	// Up wraps back to row 0.
	out, _ = m.handleKeyMain(tea.KeyMsg{Type: tea.KeyUp})
	m = out.(Model)
	if m.railCursor != 0 {
		t.Fatalf("after Up expected railCursor=0, got %d", m.railCursor)
	}
}

// TestArrowsInConversationScroll: pre-pivot, Up/Down in PaneConversation
// dispatched into m.scrollOffset (parsed-Turn scroll). After the
// embedded-PTY pivot, scrollback belongs to vt.Emulator and Phase 5
// rewires Up/Down to vt.Scrollback.Scroll(). Skipped until then.
func TestArrowsInConversationScroll(t *testing.T) {
	t.Skip("scroll moved to vt.Emulator; Phase 5 rewires PgUp/PgDn handlers.")
	turns := make([]Turn, 50)
	for i := range turns {
		turns[i] = Turn{Role: "user", Text: "msg", Ts: int64(i)}
	}
	m := Model{
		mode:               ModeMain,
		compose:            views.NewCompose(),
		groupCollapsed:     map[string]bool{},
		sessions:           []Session{{ID: "s1", Name: "alpha", Color: "12"}},
		focused:            0,
		railCursor:         0,
		activePane:         PaneConversation,
		conversation:       map[string][]Turn{"s1": turns},
		scrollOffset:       map[string]int{},
		newSinceScroll:     map[string]int{},
		lastViewportInnerW: 60,
		lastViewportInnerH: 10,
	}
	// Up scrolls; railCursor stays put.
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyUp})
	m = out.(Model)
	if m.scrollOffset["s1"] != 1 {
		t.Fatalf("Up in conversation pane should scroll, scrollOffset=%d", m.scrollOffset["s1"])
	}
	if m.railCursor != 0 {
		t.Fatalf("Up in conversation pane must not walk rail cursor, got %d", m.railCursor)
	}
}

// TestEnd_ConversationForwardsToPTY: post-pivot the embedded PTY
// (vt.Emulator) owns scrollback; the conversation pane is "claude
// mode" and forwards End to the PTY rather than mutating chubby's
// pre-pivot scrollOffset map. We can't easily assert PTY delivery
// from a unit test (no rpc.Client), but we can confirm chubby's
// own scroll state is left alone — the pane-aware refactor's
// contract.
func TestEnd_ConversationForwardsToPTY(t *testing.T) {
	t.Skip("scroll moved to vt.Emulator; conversation pane forwards End to PTY (Phase 5).")
}

// TestEnd_RailJumpsCursor: in PaneRail, End jumps the rail cursor to
// the last non-separator row.
func TestEnd_RailJumpsCursor(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "a"},
		{ID: "s2", Name: "b"},
		{ID: "s3", Name: "c"},
	}
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       sessions,
		focused:        0,
		railCursor:     0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEnd})
	m = out.(Model)
	rows := m.railRows()
	if len(rows) == 0 {
		t.Fatalf("rail rows shouldn't be empty in this test")
	}
	if m.railCursor != len(rows)-1 {
		t.Fatalf("End in rail pane should land on last row (%d), got %d", len(rows)-1, m.railCursor)
	}
}

// TestCtrlBackslash_StillCyclesSessionDirectly: the legacy
// "cycle focused session forward" power-user shortcut moved from
// Ctrl+Tab (which most terminals can't report distinctly from plain
// Tab) to Ctrl+\, alongside the existing Shift+Tab reverse cycle and
// the new pane-toggle Tab. Test name keeps the "CtrlTab" prefix so
// it lines up with the implementation plan even though the binding
// itself is Ctrl+\.
func TestCtrlTab_StillCyclesSessionDirectly(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Name: "alpha"},
		{ID: "s2", Name: "beta"},
	}
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       sessions,
		focused:        0,
		railCursor:     0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlBackslash})
	m = out.(Model)
	if m.focused != 1 {
		t.Fatalf("Ctrl+\\ should advance focused from 0 to 1, got %d", m.focused)
	}
	// activePane remains untouched.
	if m.activePane != PaneRail {
		t.Fatalf("Ctrl+\\ should not toggle pane, got %v", m.activePane)
	}
}

// TestPaneConversation_ForwardsClaudeKeysToPTY: the pane-aware
// dispatcher's contract is that every chord which conflicts with
// claude's keymap (Tab, Shift+Tab, Ctrl+R, Ctrl+D, Ctrl+T, Ctrl+L,
// Ctrl+J, Ctrl+H, Esc, Up/Down/PgUp/PgDn/Home/End/Enter) is
// forwarded to the embedded PTY when the conversation pane is
// active. We assert the side-effect we can observe without a live
// PTY: the chubby modal/chord state stays untouched (no rename,
// no release, no spawn modal, no rail navigation).
func TestPaneConversation_ForwardsClaudeKeysToPTY(t *testing.T) {
	_, cl := startFakeDaemon(t)
	cases := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"Tab", tea.KeyMsg{Type: tea.KeyTab}},
		{"ShiftTab", tea.KeyMsg{Type: tea.KeyShiftTab}},
		{"CtrlR", tea.KeyMsg{Type: tea.KeyCtrlR}},
		{"CtrlD", tea.KeyMsg{Type: tea.KeyCtrlD}},
		{"CtrlT", tea.KeyMsg{Type: tea.KeyCtrlT}},
		{"CtrlL", tea.KeyMsg{Type: tea.KeyCtrlL}},
		{"CtrlH", tea.KeyMsg{Type: tea.KeyCtrlH}},
		{"CtrlJ", tea.KeyMsg{Type: tea.KeyCtrlJ}},
		{"Esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"Up", tea.KeyMsg{Type: tea.KeyUp}},
		{"Down", tea.KeyMsg{Type: tea.KeyDown}},
		{"PgUp", tea.KeyMsg{Type: tea.KeyPgUp}},
		{"PgDown", tea.KeyMsg{Type: tea.KeyPgDown}},
		{"Home", tea.KeyMsg{Type: tea.KeyHome}},
		{"End", tea.KeyMsg{Type: tea.KeyEnd}},
		{"k", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}},
		{"j", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}},
		{"colon", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":")}},
		{"questionMark", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				client:         cl,
				mode:           ModeMain,
				compose:        views.NewCompose(),
				groupCollapsed: map[string]bool{},
				sessions: []Session{{
					ID: "s1", Name: "alpha", Status: StatusIdle, Kind: KindWrapped,
				}},
				focused:        0,
				railCursor:     0,
				activePane:     PaneConversation,
				scrollOffset:   map[string]int{},
				newSinceScroll: map[string]int{},
			}
			out, _ := m.handleKeyMain(tc.msg)
			mm := out.(Model)
			if mm.mode != ModeMain {
				t.Fatalf("%s in PaneConversation must not flip mode (got %v) — chubby modal hijack", tc.name, mm.mode)
			}
			if mm.activePane != PaneConversation {
				t.Fatalf("%s must not switch panes (got %v) — only F6 does that", tc.name, mm.activePane)
			}
			if mm.railCursor != 0 {
				t.Fatalf("%s must not walk rail cursor (got %d) — rail nav is rail-pane-only", tc.name, mm.railCursor)
			}
			if mm.pendingDeleteID != "" {
				t.Fatalf("%s must not arm two-tap release (got %q) — Ctrl+D is claude's EOF here", tc.name, mm.pendingDeleteID)
			}
		})
	}
}

// TestPaneConversation_EscEscSwitchesToRail: vim-style double-tap.
// First Esc goes to claude (cancel); second Esc within escEscWindow
// switches pane.
func TestPaneConversation_EscEscSwitchesToRail(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client:         cl,
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api", Status: StatusIdle, Kind: KindWrapped}},
		focused:        0,
		activePane:     PaneConversation,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	// First Esc: armed, pane unchanged.
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(Model)
	if m.activePane != PaneConversation {
		t.Fatalf("first Esc must not switch pane (only arm); got %v", m.activePane)
	}
	if m.lastEscAt.IsZero() {
		t.Fatalf("first Esc must stamp lastEscAt for the double-tap window")
	}
	// Second Esc within window: switches pane.
	out, _ = m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(Model)
	if m.activePane != PaneRail {
		t.Fatalf("second Esc within window must switch to rail; got %v", m.activePane)
	}
	if !m.lastEscAt.IsZero() {
		t.Fatalf("lastEscAt should clear after the toggle to prevent triple-fire")
	}
}

// TestPaneConversation_StaleEscDoesNotSwitch: a second Esc beyond
// escEscWindow re-arms instead of switching.
func TestPaneConversation_StaleEscDoesNotSwitch(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client:         cl,
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api", Status: StatusIdle, Kind: KindWrapped}},
		focused:        0,
		activePane:     PaneConversation,
		// Pretend the first Esc was way before the window.
		lastEscAt:      time.Now().Add(-time.Second),
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEsc})
	mm := out.(Model)
	if mm.activePane != PaneConversation {
		t.Fatalf("stale Esc must not switch pane; got %v", mm.activePane)
	}
	if mm.lastEscAt.IsZero() || time.Since(mm.lastEscAt) > time.Second {
		t.Fatalf("stale Esc should re-arm with a fresh timestamp")
	}
}

// TestPaneRail_EscEscSwitchesToConversation: symmetry — double-tap
// works the other way too.
func TestPaneRail_EscEscSwitchesToConversation(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client:         cl,
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api", Status: StatusIdle, Kind: KindWrapped}},
		focused:        0,
		activePane:     PaneRail,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(Model)
	out, _ = m.handleKeyMain(tea.KeyMsg{Type: tea.KeyEsc})
	mm := out.(Model)
	if mm.activePane != PaneConversation {
		t.Fatalf("Esc-Esc in rail should switch to conversation; got %v", mm.activePane)
	}
}

// TestF8_CyclesSessionForwardInBothPanes: F8 is the universal
// (every layout, every terminal) forward-cycle chord. F-keys are
// the only chord family that's reliably deliverable across
// keyboards; Ctrl+\ works on US/UK but not on Hebrew (where the
// backslash key requires AltGr that doesn't pair with Ctrl).
func TestF8_CyclesSessionForwardInBothPanes(t *testing.T) {
	paneNames := map[ActivePane]string{PaneConversation: "conversation", PaneRail: "rail"}
	for _, pane := range []ActivePane{PaneConversation, PaneRail} {
		t.Run(paneNames[pane], func(t *testing.T) {
			m := Model{
				mode:    ModeMain,
				compose: views.NewCompose(),
				sessions: []Session{
					{ID: "s1", Name: "alpha"},
					{ID: "s2", Name: "beta"},
				},
				focused:        0,
				activePane:     pane,
				groupCollapsed: map[string]bool{},
				scrollOffset:   map[string]int{},
				newSinceScroll: map[string]int{},
			}
			out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyF8})
			mm := out.(Model)
			if mm.focused != 1 {
				t.Fatalf("F8 in %v should cycle focus 0→1; got %d", paneNames[pane], mm.focused)
			}
		})
	}
}

// TestF7_CyclesSessionReverseInBothPanes: F7 mirrors F8 in the
// reverse direction.
func TestF7_CyclesSessionReverseInBothPanes(t *testing.T) {
	paneNames := map[ActivePane]string{PaneConversation: "conversation", PaneRail: "rail"}
	for _, pane := range []ActivePane{PaneConversation, PaneRail} {
		t.Run(paneNames[pane], func(t *testing.T) {
			m := Model{
				mode:    ModeMain,
				compose: views.NewCompose(),
				sessions: []Session{
					{ID: "s1", Name: "alpha"},
					{ID: "s2", Name: "beta"},
				},
				focused:        0,
				activePane:     pane,
				groupCollapsed: map[string]bool{},
				scrollOffset:   map[string]int{},
				newSinceScroll: map[string]int{},
			}
			out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyF7})
			mm := out.(Model)
			// 0 → -1 wraps to (len-1) = 1 in cycleFocusedSession.
			if mm.focused != 1 {
				t.Fatalf("F7 in %v should reverse-cycle 0→1 (wrap); got %d", paneNames[pane], mm.focused)
			}
		})
	}
}

// TestPaneConversation_F6SwitchesToRail: the one chubby chord that
// stays live in the conversation pane is F6.
func TestPaneConversation_F6SwitchesToRail(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions:       []Session{{ID: "s1", Name: "api"}},
		focused:        0,
		activePane:     PaneConversation,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyF6})
	mm := out.(Model)
	if mm.activePane != PaneRail {
		t.Fatalf("F6 in conversation must switch to rail; got %v", mm.activePane)
	}
}

// TestPaneConversation_CtrlBackslashCyclesSession: the second chubby
// chord that stays live in the conversation pane is Ctrl+\.
func TestPaneConversation_CtrlBackslashCyclesSession(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		groupCollapsed: map[string]bool{},
		sessions: []Session{
			{ID: "s1", Name: "alpha"},
			{ID: "s2", Name: "beta"},
		},
		focused:        0,
		activePane:     PaneConversation,
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	out, _ := m.handleKeyMain(tea.KeyMsg{Type: tea.KeyCtrlBackslash})
	mm := out.(Model)
	if mm.focused != 1 {
		t.Fatalf("Ctrl+\\ in conversation should cycle session forward 0→1; got %d", mm.focused)
	}
	if mm.activePane != PaneConversation {
		t.Fatalf("Ctrl+\\ must not flip pane; got %v", mm.activePane)
	}
}
