package model

import (
	"strings"
	"testing"

	"github.com/talmes1502/chubby/tui/internal/views"
)

func TestDoChubRun_NoFocusedSession(t *testing.T) {
	_, cl := startFakeDaemon(t)
	m := Model{
		client:  cl,
		focused: -1,
		mode:    ModeMain,
		compose: views.NewCompose(),
	}
	msg := runCmd(m.doChubRun("0"))
	if _, ok := msg.(composeFailedMsg); !ok {
		t.Fatalf("expected composeFailedMsg without a focused session; got %T", msg)
	}
}

func TestDoChubRun_DispatchesStartRunCommand(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "web", Status: StatusIdle, Kind: KindWrapped}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	msg := runCmd(m.doChubRun("2"))
	if _, ok := msg.(chubCommandDoneMsg); !ok {
		t.Fatalf("expected chubCommandDoneMsg; got %T (%v)", msg, msg)
	}
	d.waitForCall(t)
	method, params := d.lastCall()
	if method != "start_run_command" {
		t.Fatalf("method = %q, want start_run_command", method)
	}
	if params["session_id"] != "s1" {
		t.Fatalf("session_id = %v, want s1", params["session_id"])
	}
	// JSON-decoded numbers come back as float64 from the fakeDaemon.
	if params["index"].(float64) != 2 {
		t.Fatalf("index = %v, want 2", params["index"])
	}
}

func TestDoChubRun_RejectsBadIndex(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "web"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	for _, bad := range []string{"", "abc", "-1"} {
		msg := runCmd(m.doChubRun(bad))
		fail, ok := msg.(composeFailedMsg)
		if !ok {
			t.Fatalf("arg %q should fail; got %T", bad, msg)
		}
		if bad == "" && !strings.Contains(fail.err.Error(), "usage:") {
			t.Fatalf("empty arg should show usage; got %q", fail.err.Error())
		}
	}
	// None of those should have reached the daemon.
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if c.method == "start_run_command" {
			t.Fatalf("bad indices reached the daemon")
		}
	}
}

func TestDoChubStopRun_DispatchesStopRunCommand(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "web"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	msg := runCmd(m.doChubStopRun("0"))
	if _, ok := msg.(chubCommandDoneMsg); !ok {
		t.Fatalf("expected chubCommandDoneMsg; got %T (%v)", msg, msg)
	}
	d.waitForCall(t)
	method, params := d.lastCall()
	if method != "stop_run_command" {
		t.Fatalf("method = %q, want stop_run_command", method)
	}
	if params["index"].(float64) != 0 {
		t.Fatalf("index = %v, want 0", params["index"])
	}
}

func TestSplitChubCommand_RecognisesRun(t *testing.T) {
	cmd, arg, ok := splitChubCommand("run 0")
	if !ok || cmd != ChubCmdRun || arg != "0" {
		t.Fatalf(":run 0 should parse as (ChubCmdRun, '0'); got (%v, %q, %v)", cmd, arg, ok)
	}
	cmd2, arg2, ok2 := splitChubCommand("stop-run 1")
	if !ok2 || cmd2 != ChubCmdStopRun || arg2 != "1" {
		t.Fatalf(":stop-run 1 should parse as (ChubCmdStopRun, '1'); got (%v, %q, %v)", cmd2, arg2, ok2)
	}
}
