package model

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chubby/tui/internal/rpc"
	"github.com/USER/chubby/tui/internal/views"
)

// fakeRecentDaemon is a minimal Unix-socket JSON-RPC server scoped to
// the spawn-modal recent-cwds tests. Like fakeDaemon (chubby_commands_test.go)
// but lets the test set per-method canned results — the default fake
// returns {} which doesn't satisfy the recent_cwds shape (cwds: []string).
type fakeRecentDaemon struct {
	t        *testing.T
	listener net.Listener
	mu       sync.Mutex
	results  map[string]any // method → "result" payload
	calls    []string       // observed method names, in order
}

func startFakeRecentDaemon(t *testing.T, results map[string]any) (*fakeRecentDaemon, *rpc.Client) {
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
	d := &fakeRecentDaemon{t: t, listener: ln, results: results}
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

func (d *fakeRecentDaemon) acceptLoop() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		go d.serve(conn)
	}
}

func (d *fakeRecentDaemon) serve(conn net.Conn) {
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
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return
		}
		d.mu.Lock()
		d.calls = append(d.calls, req.Method)
		result, ok := d.results[req.Method]
		d.mu.Unlock()
		if !ok {
			result = map[string]any{}
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
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

func (d *fakeRecentDaemon) callCount(method string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := 0
	for _, m := range d.calls {
		if m == method {
			c++
		}
	}
	return c
}

// makeSpawnModel returns a Model already in ModeSpawn with the cwd
// field focused, wired to cl. Used by the Ctrl+P tests.
func makeSpawnModel(cl *rpc.Client) Model {
	m := Model{
		client:         cl,
		mode:           ModeSpawn,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
		spawn: spawnState{
			name:   views.NewSpawnNameInput(),
			cwd:    views.NewSpawnCwdInput(""),
			folder: views.NewSpawnFolderInput(""),
			field:  1, // cwd
		},
	}
	m.refocusSpawn()
	return m
}

// drainCmd executes the cmd until it produces a tea.Msg (or nil).
// tryComplete returns nil for non-cmd paths.
func drainCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// TestCtrlP_LoadsRecentCwdsAndCycles — fake daemon returns 3 cwds.
// Press Ctrl+P → first press fetches via RPC, sets cwd to result[0].
// Second press cycles to result[1] without re-fetching. Third press
// to result[2]; fourth wraps back to result[0].
func TestCtrlP_LoadsRecentCwdsAndCycles(t *testing.T) {
	d, cl := startFakeRecentDaemon(t, map[string]any{
		"recent_cwds": map[string]any{
			"cwds": []any{"/tmp/aaa", "/tmp/bbb", "/tmp/ccc"},
		},
	})
	m := makeSpawnModel(cl)

	// First Ctrl+P: triggers RPC.
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = out.(Model)
	if cmd == nil {
		t.Fatalf("expected non-nil cmd from first Ctrl+P (RPC fetch)")
	}
	msg := drainCmd(cmd)
	loaded, ok := msg.(spawnRecentCwdsLoadedMsg)
	if !ok {
		t.Fatalf("expected spawnRecentCwdsLoadedMsg, got %T (%v)", msg, msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected err: %v", loaded.err)
	}
	if len(loaded.cwds) != 3 {
		t.Fatalf("expected 3 cwds in loaded msg, got %d (%v)", len(loaded.cwds), loaded.cwds)
	}
	// Feed the loaded msg back into Update; that's what does the cwd-set + cache.
	out, _ = m.Update(loaded)
	m = out.(Model)
	if !m.spawn.recentCwdsLoaded {
		t.Fatalf("expected recentCwdsLoaded=true after first load")
	}
	if got := m.spawn.cwd.Value(); got != "/tmp/aaa" {
		t.Fatalf("after first load, cwd = %q, want /tmp/aaa", got)
	}
	if c := d.callCount("recent_cwds"); c != 1 {
		t.Fatalf("expected 1 RPC call, got %d", c)
	}

	// Second Ctrl+P: cached path. Cmd still emits a synthetic
	// loaded-msg; Update advances index to 1.
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = out.(Model)
	msg = drainCmd(cmd)
	loaded2, ok := msg.(spawnRecentCwdsLoadedMsg)
	if !ok {
		t.Fatalf("expected spawnRecentCwdsLoadedMsg on second press, got %T", msg)
	}
	out, _ = m.Update(loaded2)
	m = out.(Model)
	if got := m.spawn.cwd.Value(); got != "/tmp/bbb" {
		t.Fatalf("after second press, cwd = %q, want /tmp/bbb", got)
	}
	if c := d.callCount("recent_cwds"); c != 1 {
		t.Fatalf("second press should NOT have re-fetched; calls=%d", c)
	}

	// Third press → /tmp/ccc.
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = out.(Model)
	out, _ = m.Update(drainCmd(cmd))
	m = out.(Model)
	if got := m.spawn.cwd.Value(); got != "/tmp/ccc" {
		t.Fatalf("after third press, cwd = %q, want /tmp/ccc", got)
	}

	// Fourth press → wraps back to /tmp/aaa.
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = out.(Model)
	out, _ = m.Update(drainCmd(cmd))
	m = out.(Model)
	if got := m.spawn.cwd.Value(); got != "/tmp/aaa" {
		t.Fatalf("after wrap, cwd = %q, want /tmp/aaa", got)
	}
}

// TestCtrlP_EmptyListSurfacesError — when the daemon has no recent
// cwds, the modal stays open and shows an inline err so the user
// understands why nothing happened.
func TestCtrlP_EmptyListSurfacesError(t *testing.T) {
	_, cl := startFakeRecentDaemon(t, map[string]any{
		"recent_cwds": map[string]any{"cwds": []any{}},
	})
	m := makeSpawnModel(cl)

	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = out.(Model)
	msg := drainCmd(cmd)
	out, _ = m.Update(msg)
	m = out.(Model)
	if m.spawn.err == nil {
		t.Fatalf("expected non-nil spawn.err for empty cwds list")
	}
	if m.spawn.recentCwdsLoaded {
		// Empty result must NOT cache as "loaded" — otherwise repeated
		// Ctrl+P would mod-by-zero in the cycle path.
		t.Fatalf("empty list should not flip recentCwdsLoaded=true")
	}
	if m.mode != ModeSpawn {
		t.Fatalf("modal should stay open on empty list, got mode=%v", m.mode)
	}
}

// TestCtrlP_OnlyFiresOnCwdField — Ctrl+P in the name field (field=0)
// must NOT call recent_cwds; it should be ignored (or fall through to
// the default Ctrl+P=respawn handler in main mode, but we're in spawn
// here so it's a no-op).
func TestCtrlP_OnlyFiresOnCwdField(t *testing.T) {
	d, cl := startFakeRecentDaemon(t, map[string]any{
		"recent_cwds": map[string]any{"cwds": []any{"/tmp/a"}},
	})
	m := makeSpawnModel(cl)
	m.spawn.field = 0 // name field
	m.refocusSpawn()

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if cmd != nil {
		// The cmd MIGHT be nil from the default field text-update
		// path; if non-nil it shouldn't be the recent_cwds RPC.
		if msg := drainCmd(cmd); msg != nil {
			if _, isLoaded := msg.(spawnRecentCwdsLoadedMsg); isLoaded {
				t.Fatalf("Ctrl+P on name field should NOT trigger recent_cwds RPC")
			}
		}
	}
	// Settle: give the daemon a chance to record any unexpected call.
	time.Sleep(20 * time.Millisecond)
	if c := d.callCount("recent_cwds"); c != 0 {
		t.Fatalf("expected 0 recent_cwds calls (field=0), got %d", c)
	}
}
