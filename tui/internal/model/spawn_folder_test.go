package model

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/rpc"
	"github.com/talmes1502/chubby/tui/internal/views"
)

// startFakeDaemonReturningSessionID is a fakeDaemon variant whose
// spawn_session reply embeds {"session": {"id": <sid>}}. The folder-
// assignment branch in doSpawn only fires when it can decode that
// shape from the RPC reply, so the base startFakeDaemon (which returns
// an empty result) wouldn't exercise it. All other methods still get
// the empty default reply.
func startFakeDaemonReturningSessionID(t *testing.T, sid string) (*fakeDaemon, *rpc.Client) {
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
	d := &fakeDaemon{t: t, listener: ln}
	go func() {
		for {
			conn, err := d.listener.Accept()
			if err != nil {
				return
			}
			go serveSpawnSessionReply(conn, d, sid)
		}
	}()
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

func serveSpawnSessionReply(conn net.Conn, d *fakeDaemon, sid string) {
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

		var result map[string]any
		switch req.Method {
		case "spawn_session":
			result = map[string]any{"session": map[string]any{"id": sid}}
		default:
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

// fakeSpawnDaemon is a tiny fakeDaemon variant that replies to
// spawn_session with a session payload carrying a fixed id, so the
// folder-assign code path inside doSpawn has something to write into
// folders.json. The base startFakeDaemon returns an empty result —
// fine for most callers but useless here.
//
// We don't add a knob to the existing fakeDaemon; instead we wrap one
// connection ourselves. Since the spawn flow only calls spawn_session
// once, intercepting that single response is all we need.

// TestSpawnWithFolder_AssignsToFolder: pressing Enter in the spawn
// modal with a non-empty folder field calls spawn_session AND, on a
// successful response, writes the new session's id into folders.json
// under the named folder. The folder is created if it doesn't exist.
func TestSpawnWithFolder_AssignsToFolder(t *testing.T) {
	withTempChubbyHome(t)
	d, cl := startFakeDaemonReturningSessionID(t, "new-sid-42")

	m := Model{
		client:        cl,
		mode:          ModeSpawn,
		compose:       views.NewCompose(),
		conversation:  map[string][]Turn{},
		historyLoaded: map[string]bool{},
		spawn: spawnState{
			name:   views.NewSpawnNameInput(),
			cwd:    views.NewSpawnCwdInput("/tmp"),
			branch: views.NewSpawnBranchInput(""),
			folder: views.NewSpawnFolderInput("priority"),
			// Enter on the LAST field (folder = field 3 since the
			// branch field landed between cwd and folder) submits;
			// earlier fields advance instead (form-fill convention).
			// Start on the last field so this test exercises the
			// submit path directly.
			field: 3,
		},
	}
	m.spawn.name.SetValue("api")

	out, cmd := m.handleKeySpawn(tea.KeyMsg{Type: tea.KeyEnter})
	_ = out
	msg := runCmd(cmd)
	if _, ok := msg.(spawnDoneMsg); !ok {
		t.Fatalf("expected spawnDoneMsg, got %T (%v)", msg, msg)
	}

	// Daemon should have seen exactly one spawn_session call with empty tags.
	d.mu.Lock()
	calls := append([]fakeCall(nil), d.calls...)
	d.mu.Unlock()
	if len(calls) != 1 || calls[0].method != "spawn_session" {
		t.Fatalf("expected single spawn_session call, got %v", calls)
	}
	if tags, ok := calls[0].params["tags"].([]any); !ok || len(tags) != 0 {
		t.Fatalf("expected empty tags slice, got %v", calls[0].params["tags"])
	}

	// folders.json now contains new-sid-42 under "priority".
	on := LoadFolders()
	if got := on.FolderForSession("new-sid-42"); got != "priority" {
		t.Fatalf("expected new session in 'priority' folder, got %q (state=%v)",
			got, on.Folders)
	}
}

// TestSpawnDoneMsg_ReloadsInMemoryFolders verifies that handling a
// spawnDoneMsg refreshes m.folders from disk. Without this, doSpawn's
// SaveFolders write would land but the rail would keep showing the
// new session under "unfiled" until a manual reload.
func TestSpawnDoneMsg_ReloadsInMemoryFolders(t *testing.T) {
	withTempChubbyHome(t)

	// Pre-seed folders.json on disk as if doSpawn had just finished
	// writing — the in-memory m.folders below intentionally lags so we
	// can prove the message handler reloads it.
	st := FoldersState{Folders: map[string][]string{
		"priority": {"new-sid-42"},
	}}
	if err := SaveFolders(st); err != nil {
		t.Fatalf("SaveFolders: %v", err)
	}

	m := Model{
		mode:    ModeSpawn,
		folders: FoldersState{}, // empty — needs to be reloaded
	}
	out, _ := m.Update(spawnDoneMsg{})
	mm := out.(Model)

	if got := mm.folders.FolderForSession("new-sid-42"); got != "priority" {
		t.Fatalf("expected m.folders reloaded with 'priority' assignment for new-sid-42, "+
			"got folder=%q (state=%v)", got, mm.folders.Folders)
	}
	if mm.mode != ModeMain {
		t.Fatalf("expected mode to flip back to ModeMain, got %v", mm.mode)
	}
}

// TestSpawnWithEmptyFolder_LeavesUnfiled: empty folder field means
// the new session lands in the unfiled bucket (folders.json untouched).
func TestSpawnWithEmptyFolder_LeavesUnfiled(t *testing.T) {
	withTempChubbyHome(t)
	d, cl := startFakeDaemonReturningSessionID(t, "new-sid-99")

	m := Model{
		client:        cl,
		mode:          ModeSpawn,
		compose:       views.NewCompose(),
		conversation:  map[string][]Turn{},
		historyLoaded: map[string]bool{},
		spawn: spawnState{
			name:   views.NewSpawnNameInput(),
			cwd:    views.NewSpawnCwdInput("/tmp"),
			branch: views.NewSpawnBranchInput(""),
			folder: views.NewSpawnFolderInput(""),
			field:  3,
		},
	}
	m.spawn.name.SetValue("api")

	_, cmd := m.handleKeySpawn(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runCmd(cmd)
	if _, ok := msg.(spawnDoneMsg); !ok {
		t.Fatalf("expected spawnDoneMsg, got %T (%v)", msg, msg)
	}

	d.mu.Lock()
	calls := append([]fakeCall(nil), d.calls...)
	d.mu.Unlock()
	if len(calls) != 1 || calls[0].method != "spawn_session" {
		t.Fatalf("expected single spawn_session call, got %v", calls)
	}
	on := LoadFolders()
	if got := on.FolderForSession("new-sid-99"); got != "" {
		t.Fatalf("expected new session unfiled, got folder %q", got)
	}
}
