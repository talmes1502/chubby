package model

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chub/tui/internal/rpc"
	"github.com/USER/chub/tui/internal/views"
)

// fakeDaemon is a minimal Unix-socket JSON-RPC server used by the chub
// command tests. It records every Call (method + params) and replies
// with a successful empty result, so the model can exercise its full
// RPC path against a real *rpc.Client.
type fakeDaemon struct {
	t        *testing.T
	listener net.Listener
	mu       sync.Mutex
	calls    []fakeCall
}

type fakeCall struct {
	method string
	params map[string]any
}

func startFakeDaemon(t *testing.T) (*fakeDaemon, *rpc.Client) {
	t.Helper()
	// macOS limits Unix socket paths to ~104 chars; t.TempDir() embeds the
	// long test name and a random suffix, which can blow past that. Use
	// /tmp + an ad-hoc dir we clean up ourselves to keep the path short.
	dir, err := os.MkdirTemp("/tmp", "chub-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "f.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d := &fakeDaemon{t: t, listener: ln}
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

func (d *fakeDaemon) acceptLoop() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		go d.serve(conn)
	}
}

func (d *fakeDaemon) serve(conn net.Conn) {
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
		d.mu.Unlock()

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{},
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

func (d *fakeDaemon) lastCall() (string, map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) == 0 {
		return "", nil
	}
	c := d.calls[len(d.calls)-1]
	return c.method, c.params
}

// runCmd executes a tea.Cmd inline (the cmd is just a function returning
// a tea.Msg) so we can assert on its returned message. Returns nil if
// the cmd is nil.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// settle gives the fake daemon's goroutines a beat to record the call
// before the test reads d.calls. Without this the assertion races the
// server's recv loop on heavily loaded CI machines. The timeout is
// short — if it expires, something else is wrong.
func (d *fakeDaemon) waitForCall(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		n := len(d.calls)
		d.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("fakeDaemon: no RPC call observed within deadline")
}

// TestSendComposed_RoutesColorToRecolor — typing "/color #abcdef" must
// short-circuit into the recolor_session RPC for the focused session,
// without ever calling list_sessions/inject (those are the regular
// inject-path RPCs).
func TestSendComposed_RoutesColorToRecolor(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api", Color: "#5fafff"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/color #abcdef")
	cmd := m.sendComposed()
	msg := runCmd(cmd)
	if _, ok := msg.(chubCommandDoneMsg); !ok {
		t.Fatalf("expected chubCommandDoneMsg, got %T (%v)", msg, msg)
	}
	d.waitForCall(t)
	method, params := d.lastCall()
	if method != "recolor_session" {
		t.Fatalf("method = %q, want recolor_session", method)
	}
	if params["id"] != "s1" {
		t.Fatalf("id param = %v, want s1", params["id"])
	}
	if params["color"] != "#abcdef" {
		t.Fatalf("color param = %v, want #abcdef", params["color"])
	}
}

// TestSendComposed_ColorRejectsBadHex — argument validation runs in
// the cmd's goroutine and surfaces composeFailedMsg without contacting
// the daemon at all.
func TestSendComposed_ColorRejectsBadHex(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/color notahex")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(composeFailedMsg); !ok {
		t.Fatalf("expected composeFailedMsg, got %T (%v)", msg, msg)
	}
	d.mu.Lock()
	n := len(d.calls)
	d.mu.Unlock()
	if n != 0 {
		t.Fatalf("daemon should not have been called for invalid color, saw %d", n)
	}
}

// TestSendComposed_RoutesRenameToRenameSession — typing "/rename foo"
// invokes rename_session for the focused session id.
func TestSendComposed_RoutesRenameToRenameSession(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/rename pancake")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(chubCommandDoneMsg); !ok {
		t.Fatalf("expected chubCommandDoneMsg, got %T (%v)", msg, msg)
	}
	d.waitForCall(t)
	method, params := d.lastCall()
	if method != "rename_session" {
		t.Fatalf("method = %q, want rename_session", method)
	}
	if params["id"] != "s1" {
		t.Fatalf("id param = %v, want s1", params["id"])
	}
	if params["name"] != "pancake" {
		t.Fatalf("name param = %v, want pancake", params["name"])
	}
}

// TestSendComposed_RoutesTagToSetSessionTags — "/tag +foo -bar +baz"
// parses into add=[foo, baz] and remove=[bar] and fires
// set_session_tags for the focused session.
func TestSendComposed_RoutesTagToSetSessionTags(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/tag +foo -bar +baz")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(chubCommandDoneMsg); !ok {
		t.Fatalf("expected chubCommandDoneMsg, got %T (%v)", msg, msg)
	}
	d.waitForCall(t)
	method, params := d.lastCall()
	if method != "set_session_tags" {
		t.Fatalf("method = %q, want set_session_tags", method)
	}
	addRaw, _ := params["add"].([]any)
	removeRaw, _ := params["remove"].([]any)
	add := make([]string, 0, len(addRaw))
	for _, a := range addRaw {
		add = append(add, a.(string))
	}
	remove := make([]string, 0, len(removeRaw))
	for _, a := range removeRaw {
		remove = append(remove, a.(string))
	}
	sort.Strings(add)
	if !reflect.DeepEqual(add, []string{"baz", "foo"}) {
		t.Fatalf("add = %v, want [baz foo]", add)
	}
	if !reflect.DeepEqual(remove, []string{"bar"}) {
		t.Fatalf("remove = %v, want [bar]", remove)
	}
}

// TestSendComposed_TagRejectsEmptySpec — a /tag with no +/- tokens
// fails fast with composeFailedMsg.
func TestSendComposed_TagRejectsEmptySpec(t *testing.T) {
	d, cl := startFakeDaemon(t)
	m := Model{
		client:   cl,
		sessions: []Session{{ID: "s1", Name: "api"}},
		focused:  0,
		mode:     ModeMain,
		compose:  views.NewCompose(),
	}
	m.compose.SetValue("/tag    ")
	msg := runCmd(m.sendComposed())
	if _, ok := msg.(composeFailedMsg); !ok {
		t.Fatalf("expected composeFailedMsg, got %T (%v)", msg, msg)
	}
	d.mu.Lock()
	n := len(d.calls)
	d.mu.Unlock()
	if n != 0 {
		t.Fatalf("daemon should not have been called, saw %d", n)
	}
}

// Sanity: the fake daemon can be reached over a real Client.Call so the
// test infra itself isn't silently broken.
func TestFakeDaemon_BasicCall(t *testing.T) {
	d, cl := startFakeDaemon(t)
	if _, err := cl.Call(context.Background(), "ping", nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	method, _ := d.lastCall()
	if method != "ping" {
		t.Fatalf("method = %q, want ping", method)
	}
}
