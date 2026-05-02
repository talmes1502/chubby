package model

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/USER/chubby/tui/internal/rpc"
	"github.com/USER/chubby/tui/internal/views"
)

// fakeReleaseDaemon is a fake RPC server that replies to
// ``release_session`` with a canned (claude_session_id, cwd) payload
// and records the params it received. Mirrors the chubby_commands_test
// fakeDaemon but with a custom result for one method — the generic
// fakeDaemon always replies with an empty result, which would make the
// detach handler bail out at "daemon returned no claude_session_id".
type fakeReleaseDaemon struct {
	t            *testing.T
	listener     net.Listener
	mu           sync.Mutex
	calls        []fakeCall
	releaseReply map[string]any // result payload for release_session
	releaseErr   *struct {
		code    int
		message string
	}
}

func startFakeReleaseDaemon(t *testing.T, claudeSID, cwd string) (*fakeReleaseDaemon, *rpc.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "chubby-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "f.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d := &fakeReleaseDaemon{
		t:        t,
		listener: ln,
		releaseReply: map[string]any{
			"claude_session_id": claudeSID,
			"cwd":               cwd,
		},
	}
	go d.acceptLoop()
	cl, err := rpc.Dial(sock)
	if err != nil {
		ln.Close()
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		cl.Close()
		ln.Close()
	})
	return d, cl
}

func (d *fakeReleaseDaemon) acceptLoop() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		go d.serve(conn)
	}
}

func (d *fakeReleaseDaemon) serve(conn net.Conn) {
	defer conn.Close()
	for {
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr)
		body := make([]byte, n)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return
		}
		d.mu.Lock()
		d.calls = append(d.calls, fakeCall{method: req.Method, params: req.Params})
		errSpec := d.releaseErr
		d.mu.Unlock()

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
		}
		if req.Method == "release_session" && errSpec != nil {
			resp["error"] = map[string]any{
				"code":    errSpec.code,
				"message": errSpec.message,
			}
		} else if req.Method == "release_session" {
			resp["result"] = d.releaseReply
		} else {
			resp["result"] = map[string]any{}
		}
		out, _ := json.Marshal(resp)
		header := make([]byte, 4)
		binary.BigEndian.PutUint32(header, uint32(len(out)))
		if _, err := conn.Write(header); err != nil {
			return
		}
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

func (d *fakeReleaseDaemon) lastCall() (string, map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) == 0 {
		return "", nil
	}
	c := d.calls[len(d.calls)-1]
	return c.method, c.params
}

// TestSendComposed_RoutesDetachToReleaseRPC — typing "detach" with a
// focused session calls release_session over RPC, then invokes
// openExternalClaudeFn with the captured (claude_session_id, cwd). On
// success we surface chubCommandDoneMsg with a "released" toast.
func TestSendComposed_RoutesDetachToReleaseRPC(t *testing.T) {
	d, cl := startFakeReleaseDaemon(t,
		"abcdef01-0000-0000-0000-000000000000",
		"/tmp/proj",
	)
	prev := openExternalClaudeFn
	t.Cleanup(func() { openExternalClaudeFn = prev })
	var capturedSID, capturedCwd string
	openExternalClaudeFn = func(sid, cwd string) error {
		capturedSID = sid
		capturedCwd = cwd
		return nil
	}
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("detach")
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
	if !strings.Contains(done.toast, "released") {
		t.Fatalf("toast %q should mention 'released'", done.toast)
	}
	method, params := d.lastCall()
	if method != "release_session" {
		t.Fatalf("method = %q, want release_session", method)
	}
	if params["id"] != "s1" {
		t.Fatalf("id param = %v, want s1", params["id"])
	}
	if capturedSID != "abcdef01-0000-0000-0000-000000000000" {
		t.Fatalf("openExternalClaudeFn sid = %q, want abcdef01-...", capturedSID)
	}
	if capturedCwd != "/tmp/proj" {
		t.Fatalf("openExternalClaudeFn cwd = %q, want /tmp/proj", capturedCwd)
	}
}

// TestDetach_NoFocusedSessionFails — /detach with no focused session
// surfaces composeFailedMsg and never contacts the daemon.
func TestDetach_NoFocusedSessionFails(t *testing.T) {
	d, cl := startFakeReleaseDaemon(t, "", "")
	prev := openExternalClaudeFn
	t.Cleanup(func() { openExternalClaudeFn = prev })
	called := false
	openExternalClaudeFn = func(sid, cwd string) error {
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
	m.compose.SetValue("detach")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(composeFailedMsg); !ok {
		t.Fatalf("expected composeFailedMsg, got %T (%v)", msg, msg)
	}
	if called {
		t.Fatal("openExternalClaudeFn must not run when nothing is focused")
	}
	d.mu.Lock()
	n := len(d.calls)
	d.mu.Unlock()
	if n != 0 {
		t.Fatalf("daemon must not be contacted when no session is focused, saw %d calls", n)
	}
}

// TestDetach_RPCErrorBecomesComposeFailed — when release_session
// returns an error (e.g. session has no bound claude_session_id yet),
// the user gets composeFailedMsg with "detach failed:" prefix.
func TestDetach_RPCErrorBecomesComposeFailed(t *testing.T) {
	d, cl := startFakeReleaseDaemon(t, "", "")
	d.mu.Lock()
	d.releaseErr = &struct {
		code    int
		message string
	}{code: 1010, message: "session has no bound claude session id yet — wait a moment and retry"}
	d.mu.Unlock()
	prev := openExternalClaudeFn
	t.Cleanup(func() { openExternalClaudeFn = prev })
	called := false
	openExternalClaudeFn = func(sid, cwd string) error {
		called = true
		return nil
	}
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("detach")
	msg := runCmd(m.sendComposed())
	failed, ok := msg.(composeFailedMsg)
	if !ok {
		t.Fatalf("expected composeFailedMsg, got %T (%v)", msg, msg)
	}
	if !strings.Contains(failed.err.Error(), "detach failed") {
		t.Fatalf("error should be wrapped with 'detach failed': %v", failed.err)
	}
	if called {
		t.Fatal("openExternalClaudeFn must not run when the daemon RPC failed")
	}
}

// TestDetach_SpawnErrorStillReleases — when openExternalClaudeFn
// fails, we still return chubCommandDoneMsg (NOT composeFailedMsg)
// because the daemon-side release already succeeded; the toast warns
// the user the new window couldn't be opened so they can do it
// manually.
func TestDetach_SpawnErrorStillReleases(t *testing.T) {
	_, cl := startFakeReleaseDaemon(t,
		"abcdef01-0000-0000-0000-000000000000",
		"/tmp/proj",
	)
	prev := openExternalClaudeFn
	t.Cleanup(func() { openExternalClaudeFn = prev })
	openExternalClaudeFn = func(sid, cwd string) error {
		return fmt.Errorf("no GUI terminal found")
	}
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("detach")
	msg := runCmd(m.sendComposed())
	done, ok := msg.(chubCommandDoneMsg)
	if !ok {
		t.Fatalf("expected chubCommandDoneMsg (release succeeded server-side), got %T (%v)", msg, msg)
	}
	if !strings.Contains(done.toast, "released api") {
		t.Fatalf("toast %q should confirm release", done.toast)
	}
	if !strings.Contains(done.toast, "could not open new window") {
		t.Fatalf("toast %q should mention spawn-window failure", done.toast)
	}
}

// TestSplitChubCommand_RecognizesDetach — the splitter must surface
// "detach" as a chub-side command head so sendComposed routes it
// before the inject path. (Belt-and-suspenders for the longest-first
// invariant: a future "/detach-foo" wouldn't change this test's
// behaviour because we pass the bare head.)
func TestSplitChubCommand_RecognizesDetach(t *testing.T) {
	cmd, arg, ok := splitChubCommand("detach")
	if !ok {
		t.Fatal("expected /detach to be recognised as a chub command")
	}
	if cmd != "detach" {
		t.Fatalf("cmd = %q, want detach", cmd)
	}
	if arg != "" {
		t.Fatalf("arg = %q, want empty", arg)
	}
}

// TestModelNew_StartupFocusFromEnv — CHUBBY_FOCUS_SESSION populates
// startupFocusName so the first listMsg can resolve it. CHUBBY_DETACHED=1
// forces railCollapsed=true at startup. We use t.Setenv so the env
// vars unset themselves at test-end without polluting other tests.
// (These flags are no longer driven by /detach but the env-var path
// is still supported for manual `chubby tui --focus`.)
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
