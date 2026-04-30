package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chub/tui/internal/views"
)

// TestAutoSpawnDefault_OnFirstEmptyList verifies that the very first
// listMsg with zero sessions schedules an auto-spawn (NOT a modal flip)
// so the user lands on a working session instead of staring at a blank
// viewport. The mode must remain ModeMain — we only flip to ModeSpawn
// on the explicit fallback message.
func TestAutoSpawnDefault_OnFirstEmptyList(t *testing.T) {
	d, cl := startFakeDaemon(t)

	m := Model{
		client:         cl,
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	out, cmd := m.Update(listMsg(nil))
	m2 := out.(Model)
	if m2.mode != ModeMain {
		t.Fatalf("expected ModeMain (auto-spawn does NOT flip mode), got %v", m2.mode)
	}
	if !m2.initialListReceived {
		t.Fatalf("initialListReceived should be true after first listMsg")
	}
	if cmd == nil {
		t.Fatalf("expected a non-nil tea.Cmd batch (auto-spawn + listenEvents)")
	}

	// Run the batch; one of the produced messages should be
	// autoSpawnedMsg (or, on a daemon error, autoSpawnFallbackMsg —
	// neither is a hard failure here, but we want to see the spawn
	// RPC actually fire).
	collectFromBatch(t, cmd)
	d.waitForCall(t)

	// Find the spawn_session call in the recorded calls. There may be
	// other RPCs in flight (list_sessions from refreshSessions, etc.).
	d.mu.Lock()
	defer d.mu.Unlock()
	var found bool
	for _, c := range d.calls {
		if c.method != "spawn_session" {
			continue
		}
		found = true
		if c.params["name"] != "temp" {
			t.Fatalf("auto-spawn name = %v, want temp", c.params["name"])
		}
		if cwd, _ := c.params["cwd"].(string); cwd == "" {
			t.Fatalf("auto-spawn cwd should be non-empty (got %q)", cwd)
		}
		break
	}
	if !found {
		t.Fatalf("expected a spawn_session RPC; recorded calls: %v", d.calls)
	}
}

// TestAutoSpawnDefault_NotFiredWhenSessionsExist verifies the
// auto-spawn does NOT run when the first list contains sessions —
// the user has work to focus on.
func TestAutoSpawnDefault_NotFiredWhenSessionsExist(t *testing.T) {
	d, cl := startFakeDaemon(t)

	m := Model{
		client:         cl,
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	out, cmd := m.Update(listMsg([]Session{
		{ID: "s1", Name: "alpha", Cwd: "/tmp", Status: "idle"},
	}))
	m2 := out.(Model)
	if m2.mode != ModeMain {
		t.Fatalf("expected ModeMain when first list has sessions, got %v", m2.mode)
	}
	if !m2.initialListReceived {
		t.Fatalf("initialListReceived should be true after any listMsg")
	}
	collectFromBatch(t, cmd)

	// No spawn_session call should have been made.
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if c.method == "spawn_session" {
			t.Fatalf("did not expect spawn_session, but it was called: %v", c.params)
		}
	}
}

// TestAutoSpawnDefault_NotRefiredAfterSecondEmpty verifies the auto-spawn
// fires once: after dismissing the first empty list, a second empty
// listMsg must NOT trigger another spawn.
func TestAutoSpawnDefault_NotRefiredAfterSecondEmpty(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:         cl,
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	out, cmd := m.Update(listMsg(nil))
	m = out.(Model)
	collectFromBatch(t, cmd)
	d.waitForCall(t)

	d.mu.Lock()
	firstSpawnCount := 0
	for _, c := range d.calls {
		if c.method == "spawn_session" {
			firstSpawnCount++
		}
	}
	d.mu.Unlock()
	if firstSpawnCount == 0 {
		t.Fatalf("expected one spawn_session after first empty list")
	}

	// Second empty listMsg must not auto-spawn again.
	out, cmd = m.Update(listMsg(nil))
	collectFromBatch(t, cmd)

	d.mu.Lock()
	defer d.mu.Unlock()
	secondSpawnCount := 0
	for _, c := range d.calls {
		if c.method == "spawn_session" {
			secondSpawnCount++
		}
	}
	if secondSpawnCount != firstSpawnCount {
		t.Fatalf("auto-spawn re-fired: spawn_session count went from %d to %d",
			firstSpawnCount, secondSpawnCount)
	}
}

// TestAutoSpawnFallback_OpensSpawnModal verifies that an
// autoSpawnFallbackMsg flips the mode to ModeSpawn so the user can pick
// a different name/cwd when the auto-spawn path can't make progress.
func TestAutoSpawnFallback_OpensSpawnModal(t *testing.T) {
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	out, _ := m.Update(autoSpawnFallbackMsg{})
	m2 := out.(Model)
	if m2.mode != ModeSpawn {
		t.Fatalf("expected ModeSpawn after autoSpawnFallbackMsg, got %v", m2.mode)
	}
}

// collectFromBatch fires the cmds inside a tea.Batch (or a single bare
// cmd) so the underlying RPCs actually execute. Each cmd runs in its
// own goroutine because some (m.listenEvents) block forever waiting on
// a channel; the fakeDaemon never pushes events. The test inspects the
// daemon side directly via the call log, so we don't need return values.
//
// When the model returns a Batch with N>=2 cmds, calling it once
// produces a tea.BatchMsg slice (the cmds remain unevaluated). When it
// returns a single cmd (compactCmds collapses N=1 batches), we get
// that bare cmd back directly — we must NOT eagerly call it on the
// caller's goroutine.
func collectFromBatch(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	// Run the outer cmd in a goroutine. It either returns a BatchMsg
	// quickly (compose case) or blocks forever (bare listenEvents case).
	resultCh := make(chan tea.Msg, 1)
	go func() {
		defer func() { _ = recover() }()
		resultCh <- cmd()
	}()
	var msg tea.Msg
	select {
	case msg = <-resultCh:
	case <-time.After(100 * time.Millisecond):
		// Outer cmd is blocking; it's a single non-batch cmd we don't care
		// about (e.g. listenEvents). Nothing further to do.
		return
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		go func(fn tea.Cmd) {
			defer func() { _ = recover() }()
			_ = fn()
		}(c)
	}
}
