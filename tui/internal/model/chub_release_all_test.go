package model

import (
	"strings"
	"testing"

	"github.com/USER/chubby/tui/internal/views"
)

// Bare ":release-all" must NOT actually release anything — it returns
// a composeFailedMsg whose error message previews the count, so the
// user sees what they're about to do before confirming.
func TestDoChubReleaseAll_BareRefusesAndPreviewsCount(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client: cl,
		sessions: []Session{
			{ID: "s1", Name: "web", Status: StatusIdle, Kind: KindWrapped},
			{ID: "s2", Name: "api", Status: StatusAwaitingUser, Kind: KindWrapped},
			{ID: "s3", Name: "obs", Status: StatusDead, Kind: KindWrapped},
			{ID: "s4", Name: "ro", Status: StatusIdle, Kind: KindReadonly},
		},
		focused: 0,
		mode:    ModeMain,
		compose: views.NewCompose(),
	}
	msg := runCmd(m.doChubReleaseAll(""))
	fail, ok := msg.(composeFailedMsg)
	if !ok {
		t.Fatalf("bare :release-all should return composeFailedMsg; got %T (%v)", msg, msg)
	}
	if !strings.Contains(fail.err.Error(), "2 session") {
		t.Fatalf("preview should mention count of 2 (skipping dead+readonly); got %q", fail.err.Error())
	}
	if !strings.Contains(fail.err.Error(), "release-all confirm") {
		t.Fatalf("preview should tell the user how to confirm; got %q", fail.err.Error())
	}
}

// ":release-all confirm" runs full teardown on every live, non-readonly
// session. The fakeDaemon happily returns success for every RPC, so we
// expect one release_session call per eligible session.
func TestDoChubReleaseAll_ConfirmReleasesAllLive(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client: cl,
		sessions: []Session{
			{ID: "s1", Name: "web", Status: StatusIdle, Kind: KindWrapped},
			{ID: "s2", Name: "api", Status: StatusAwaitingUser, Kind: KindWrapped},
			{ID: "dead", Name: "obs", Status: StatusDead, Kind: KindWrapped},
			{ID: "ro", Name: "watcher", Status: StatusIdle, Kind: KindReadonly},
		},
		focused: 0,
		mode:    ModeMain,
		compose: views.NewCompose(),
	}
	msg := runCmd(m.doChubReleaseAll("confirm"))
	if _, ok := msg.(chubCommandDoneMsg); !ok {
		t.Fatalf("expected chubCommandDoneMsg; got %T (%v)", msg, msg)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	releasedIDs := map[string]bool{}
	for _, c := range d.calls {
		if c.method == "release_session" {
			if id, _ := c.params["id"].(string); id != "" {
				releasedIDs[id] = true
			}
		}
	}
	if len(releasedIDs) != 2 || !releasedIDs["s1"] || !releasedIDs["s2"] {
		t.Fatalf("expected release_session for s1+s2 only; got %v", releasedIDs)
	}
}

// With no live sessions, both bare and confirmed forms should refuse
// rather than emit a misleading "released 0" toast.
func TestDoChubReleaseAll_NoLiveSessions(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client: cl,
		sessions: []Session{
			{ID: "dead1", Name: "z1", Status: StatusDead, Kind: KindWrapped},
			{ID: "ro1", Name: "watcher", Status: StatusIdle, Kind: KindReadonly},
		},
		focused: 0,
		mode:    ModeMain,
		compose: views.NewCompose(),
	}
	for _, arg := range []string{"", "confirm"} {
		msg := runCmd(m.doChubReleaseAll(arg))
		fail, ok := msg.(composeFailedMsg)
		if !ok {
			t.Fatalf("with no live sessions, arg=%q should fail; got %T", arg, msg)
		}
		if !strings.Contains(fail.err.Error(), "no live sessions") {
			t.Fatalf("expected 'no live sessions' message; got %q", fail.err.Error())
		}
	}
}

// release-all must dispatch through the standard ChubCommand router
// just like clone / detach / restart, so palette parsing routes it
// correctly. The rail palette strips its ":" prefix before calling
// splitChubCommand, so we test the bare form here.
func TestSplitChubCommand_RecognisesReleaseAll(t *testing.T) {
	cmd, arg, ok := splitChubCommand("release-all confirm")
	if !ok {
		t.Fatalf("splitChubCommand should recognise 'release-all'")
	}
	if cmd != ChubCmdReleaseAll {
		t.Fatalf("cmd = %v, want ChubCmdReleaseAll", cmd)
	}
	if arg != "confirm" {
		t.Fatalf("arg = %q, want 'confirm'", arg)
	}
	// And the slash-prefixed form (legacy inline-in-claude usage).
	cmd2, arg2, ok2 := splitChubCommand("/release-all")
	if !ok2 || cmd2 != ChubCmdReleaseAll || arg2 != "" {
		t.Fatalf("'/release-all' should parse as (ChubCmdReleaseAll, ''); got (%v, %q, %v)", cmd2, arg2, ok2)
	}
}
