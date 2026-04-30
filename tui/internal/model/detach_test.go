package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/USER/chubby/tui/internal/views"
)

// TestSendComposed_RoutesDetachToOpenWindow — typing "/detach" with a
// focused session calls our stubbed openDetachedFn and surfaces a
// chubCommandDoneMsg with a non-empty toast. We never touch a real
// daemon RPC: /detach is a pure chub-side command, so the inject path
// must NOT fire (hence d.calls stays empty).
func TestSendComposed_RoutesDetachToOpenWindow(t *testing.T) {
	d, cl := startFakeDaemon(t)
	prev := openDetachedFn
	t.Cleanup(func() { openDetachedFn = prev })
	var captured string
	openDetachedFn = func(name string) error {
		captured = name
		return nil
	}
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/detach")
	msg := runCmd(m.sendComposed())
	done, ok := msg.(chubCommandDoneMsg)
	if !ok {
		t.Fatalf("expected chubCommandDoneMsg, got %T (%v)", msg, msg)
	}
	if done.toast == "" {
		t.Fatalf("expected non-empty toast, got %q", done.toast)
	}
	if !strings.Contains(done.toast, "api") {
		t.Fatalf("toast %q should reference focused session name", done.toast)
	}
	if captured != "api" {
		t.Fatalf("openDetachedFn called with %q, want api", captured)
	}
	d.mu.Lock()
	n := len(d.calls)
	d.mu.Unlock()
	if n != 0 {
		t.Fatalf("/detach must not contact the daemon, saw %d calls", n)
	}
}

// TestSendComposed_DetachWithNoFocusFailsCleanly — /detach with no
// focused session surfaces a composeFailedMsg without invoking the
// spawn helper.
func TestSendComposed_DetachWithNoFocusFailsCleanly(t *testing.T) {
	_, cl := startFakeDaemon(t)
	prev := openDetachedFn
	t.Cleanup(func() { openDetachedFn = prev })
	called := false
	openDetachedFn = func(name string) error {
		called = true
		return nil
	}
	m := Model{
		client:   cl,
		sessions: []Session{}, // no sessions => focusedSession() == nil
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/detach")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(composeFailedMsg); !ok {
		t.Fatalf("expected composeFailedMsg, got %T (%v)", msg, msg)
	}
	if called {
		t.Fatal("openDetachedFn must not run when nothing is focused")
	}
}

// TestSendComposed_DetachSpawnErrorBecomesComposeFailed — when the
// terminal-spawn helper returns an error (e.g. no GUI terminal on
// Linux), the user gets a composeFailedMsg with the underlying error
// wrapped in "detach failed: ..." so the cause is visible in the
// status bar.
func TestSendComposed_DetachSpawnErrorBecomesComposeFailed(t *testing.T) {
	_, cl := startFakeDaemon(t)
	prev := openDetachedFn
	t.Cleanup(func() { openDetachedFn = prev })
	openDetachedFn = func(name string) error {
		return fmt.Errorf("no GUI terminal found")
	}
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/detach")
	msg := runCmd(m.sendComposed())
	failed, ok := msg.(composeFailedMsg)
	if !ok {
		t.Fatalf("expected composeFailedMsg, got %T (%v)", msg, msg)
	}
	if !strings.Contains(failed.err.Error(), "detach failed") {
		t.Fatalf("error should be wrapped with 'detach failed': %v", failed.err)
	}
	if !strings.Contains(failed.err.Error(), "no GUI terminal found") {
		t.Fatalf("error should preserve underlying cause: %v", failed.err)
	}
}

// TestSplitChubCommand_RecognizesDetach — the splitter must surface
// "/detach" as a chub-side command head so sendComposed routes it
// before the inject path. (Belt-and-suspenders for the longest-first
// invariant: a future "/detach-foo" wouldn't change this test's
// behaviour because we pass the bare head.)
func TestSplitChubCommand_RecognizesDetach(t *testing.T) {
	cmd, arg, ok := splitChubCommand("/detach")
	if !ok {
		t.Fatal("expected /detach to be recognised as a chub command")
	}
	if cmd != "/detach" {
		t.Fatalf("cmd = %q, want /detach", cmd)
	}
	if arg != "" {
		t.Fatalf("arg = %q, want empty", arg)
	}
}

// TestModelNew_StartupFocusFromEnv — CHUBBY_FOCUS_SESSION populates
// startupFocusName so the first listMsg can resolve it. CHUBBY_DETACHED=1
// forces railCollapsed=true at startup. We use t.Setenv so the env
// vars unset themselves at test-end without polluting other tests.
func TestModelNew_StartupFocusFromEnv(t *testing.T) {
	_, cl := startFakeDaemon(t)
	t.Setenv("CHUBBY_FOCUS_SESSION", "api")
	t.Setenv("CHUBBY_DETACHED", "1")
	m := New(cl)
	if m.startupFocusName != "api" {
		t.Fatalf("startupFocusName = %q, want api", m.startupFocusName)
	}
	if !m.railCollapsed {
		t.Fatal("CHUBBY_DETACHED=1 should force railCollapsed=true")
	}
}

// TestListMsg_ResolvesStartupFocus — when the first listMsg arrives,
// startupFocusName is consumed: m.focused jumps to the matching
// session's index and the field is cleared so a later list refresh
// (e.g. after a rename) doesn't re-snap the user's focus.
func TestListMsg_ResolvesStartupFocus(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client:           cl,
		conversation:     map[string][]Turn{},
		mode:             ModeMain,
		compose:          views.NewCompose(),
		historyLoaded:    map[string]bool{},
		scrollOffset:     map[string]int{},
		newSinceScroll:   map[string]int{},
		lastUsage:        map[string]sessionUsage{},
		startupFocusName: "second",
	}
	updated, _ := m.Update(listMsg([]Session{
		{ID: "s1", Name: "first"},
		{ID: "s2", Name: "second"},
		{ID: "s3", Name: "third"},
	}))
	m2 := updated.(Model)
	if m2.focused != 1 {
		t.Fatalf("focused = %d, want 1 (the 'second' session)", m2.focused)
	}
	if m2.startupFocusName != "" {
		t.Fatalf("startupFocusName should be cleared after first resolve, got %q", m2.startupFocusName)
	}
}

// TestListMsg_StartupFocusNoMatchKeepsDefaultFocus — if the focus
// name doesn't match any session (e.g. user typed wrong name into
// `chubby tui --focus`), we leave focused at its default and clear
// the field so we don't keep retrying on every refresh.
func TestListMsg_StartupFocusNoMatchKeepsDefaultFocus(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client:           cl,
		conversation:     map[string][]Turn{},
		mode:             ModeMain,
		compose:          views.NewCompose(),
		historyLoaded:    map[string]bool{},
		scrollOffset:     map[string]int{},
		newSinceScroll:   map[string]int{},
		lastUsage:        map[string]sessionUsage{},
		startupFocusName: "nonesuch",
	}
	updated, _ := m.Update(listMsg([]Session{
		{ID: "s1", Name: "first"},
	}))
	m2 := updated.(Model)
	if m2.focused != 0 {
		t.Fatalf("focused = %d, want 0 (default)", m2.focused)
	}
	if m2.startupFocusName != "" {
		t.Fatal("startupFocusName should still be cleared even when no match")
	}
}
