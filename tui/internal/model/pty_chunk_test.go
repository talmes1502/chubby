package model

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/USER/chubby/tui/internal/rpc"
)

// TestPtyChunkEvent_CreatesPaneAndWritesChunk — when the daemon
// broadcasts a pty_chunk for a session we haven't seen before, the
// reducer must lazy-create a Pane in m.pty[sid] and write the
// decoded bytes into it. The pane's View() should subsequently
// reflect those bytes.
func TestPtyChunkEvent_CreatesPaneAndWritesChunk(t *testing.T) {
	m := Model{
		conversation: map[string][]Turn{},
	}
	chunk := []byte("hello pane")
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "pty_chunk",
			"event_params": map[string]any{
				"session_id": "s1",
				"chunk_b64":  base64.StdEncoding.EncodeToString(chunk),
				"role":       "assistant",
				"ts":         float64(1),
			},
		},
	})
	out, _ := m.Update(ev)
	mm := out.(Model)

	pane := mm.pty["s1"]
	if pane == nil {
		t.Fatalf("expected pty pane to be created for session s1")
	}
	view := pane.View()
	if !strings.Contains(view, "hello pane") {
		t.Fatalf("expected chunk content in pane view, got %q", view)
	}
}

// TestPtyChunkEvent_AppendsToExistingPane — a second pty_chunk for
// the same session should append to the existing pane, not replace
// or recreate it.
func TestPtyChunkEvent_AppendsToExistingPane(t *testing.T) {
	m := Model{
		conversation: map[string][]Turn{},
	}
	first := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "pty_chunk",
			"event_params": map[string]any{
				"session_id": "s1",
				"chunk_b64":  base64.StdEncoding.EncodeToString([]byte("first ")),
				"ts":         float64(1),
			},
		},
	})
	out, _ := m.Update(first)
	m = out.(Model)
	originalPane := m.pty["s1"]

	second := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "pty_chunk",
			"event_params": map[string]any{
				"session_id": "s1",
				"chunk_b64":  base64.StdEncoding.EncodeToString([]byte("second")),
				"ts":         float64(2),
			},
		},
	})
	out, _ = m.Update(second)
	m = out.(Model)

	if m.pty["s1"] != originalPane {
		t.Fatalf("expected same pane after second chunk, got new instance")
	}
	view := m.pty["s1"].View()
	if !strings.Contains(view, "first") || !strings.Contains(view, "second") {
		t.Fatalf("expected both chunks in pane view, got %q", view)
	}
}

// TestPtyReplayMsg_PrimesPane — a ptyReplayMsg from get_pty_buffer
// should feed its chunk into the pane the same way live pty_chunks
// do, creating the pane if needed.
func TestPtyReplayMsg_PrimesPane(t *testing.T) {
	m := Model{
		conversation: map[string][]Turn{},
	}
	out, _ := m.Update(ptyReplayMsg{sid: "s1", chunk: []byte("replayed history")})
	mm := out.(Model)
	pane := mm.pty["s1"]
	if pane == nil {
		t.Fatalf("expected pane created from replay")
	}
	if !strings.Contains(pane.View(), "replayed history") {
		t.Fatalf("expected replay content in view, got %q", pane.View())
	}
}
