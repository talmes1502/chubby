package model

import (
	"reflect"
	"testing"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// TestSendComposed_RoutesMoveToFolder: typing "/movetofolder backend"
// updates folders.json with the focused session under the named
// folder. Folders state is TUI-local — no RPC to the daemon.
func TestSendComposed_RoutesMoveToFolder(t *testing.T) {
	withTempChubbyHome(t)
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/movetofolder backend")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(chubCommandDoneMsg); !ok {
		t.Fatalf("expected chubCommandDoneMsg, got %T (%v)", msg, msg)
	}
	// folders.json should now contain s1 under "backend".
	on := LoadFolders()
	if !reflect.DeepEqual(on.SessionsInFolder("backend"), []string{"s1"}) {
		t.Fatalf("backend folder should contain s1, got %v", on.Folders)
	}
	// Daemon must NOT have been hit — folders are TUI-local.
	d.mu.Lock()
	n := len(d.calls)
	d.mu.Unlock()
	if n != 0 {
		t.Fatalf("daemon should not have been called for /movetofolder, saw %d", n)
	}
}

// TestSendComposed_MoveToFolderRejectsEmptyArg: bare /movetofolder
// surfaces composeFailedMsg.
func TestSendComposed_MoveToFolderRejectsEmptyArg(t *testing.T) {
	withTempChubbyHome(t)
	_, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("movetofolder")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(composeFailedMsg); !ok {
		t.Fatalf("expected composeFailedMsg, got %T (%v)", msg, msg)
	}
}

// TestSendComposed_RoutesRemoveFromFolder: /removefromfolder yanks
// the focused session out of any folder.
func TestSendComposed_RoutesRemoveFromFolder(t *testing.T) {
	withTempChubbyHome(t)
	// Pre-seed: s1 in folder "backend".
	pre := FoldersState{Folders: map[string][]string{}}
	pre.Assign("backend", "s1")
	if err := SaveFolders(pre); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("removefromfolder")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(chubCommandDoneMsg); !ok {
		t.Fatalf("expected chubCommandDoneMsg, got %T (%v)", msg, msg)
	}
	on := LoadFolders()
	if got := on.FolderForSession("s1"); got != "" {
		t.Fatalf("expected s1 unassigned, still in %q", got)
	}
}

// TestSplitChubCommand_MovetoFolderArg: ensure the longest-first
// split doesn't accidentally cut "/movetofolder backend" into
// /move + tofolder backend or anything else weird.
func TestSplitChubCommand_MovetoFolderArg(t *testing.T) {
	cmd, arg, ok := splitChubCommand("/movetofolder backend")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if cmd != "movetofolder" {
		t.Fatalf("cmd=%q want movetofolder", cmd)
	}
	if arg != "backend" {
		t.Fatalf("arg=%q want backend", arg)
	}
}

// TestSplitChubCommand_RemoveFromFolderNoArg: bare command with no
// arg is recognized.
func TestSplitChubCommand_RemoveFromFolderNoArg(t *testing.T) {
	cmd, arg, ok := splitChubCommand("removefromfolder")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if cmd != "removefromfolder" {
		t.Fatalf("cmd=%q want removefromfolder", cmd)
	}
	if arg != "" {
		t.Fatalf("arg=%q want empty", arg)
	}
}
