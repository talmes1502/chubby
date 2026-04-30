package model

import (
	"testing"

	"github.com/USER/chubby/tui/internal/rpc"
)

// TestHistoryTurnsMsg_ReplacesConversation drives the reducer with a
// historyTurnsMsg and verifies it overwrites m.conversation[sid] with
// the loaded turns. Live events arriving after this should append on
// top — that's covered by the existing transcript_message tests.
func TestHistoryTurnsMsg_ReplacesConversation(t *testing.T) {
	m := Model{
		conversation: map[string][]Turn{
			// Pre-existing partial state — must be replaced wholesale.
			"s_abc": {{Role: "user", Text: "stale", Ts: 1}},
		},
		historyLoaded: map[string]bool{},
	}
	loaded := []Turn{
		{Role: "user", Text: "hello", Ts: 100},
		{Role: "assistant", Text: "hi there", Ts: 101},
	}
	out, _ := m.Update(historyTurnsMsg{sid: "s_abc", turns: loaded})
	got := out.(Model).conversation["s_abc"]
	if len(got) != 2 {
		t.Fatalf("want 2 turns, got %d", len(got))
	}
	if got[0].Text != "hello" || got[1].Text != "hi there" {
		t.Fatalf("unexpected turns: %+v", got)
	}
}

// TestListMsg_MarksHistoryLoadedForEachSession verifies that on a
// listMsg the reducer flips historyLoaded[sid]=true for every session
// in the list. We can't easily assert the actual RPC fired (no client
// in test), but the loaded-flag is what guards against re-firing on
// every 2-second tick refresh — that's the critical contract.
func TestListMsg_MarksHistoryLoadedForEachSession(t *testing.T) {
	m := Model{
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
	}
	sessions := []Session{
		{ID: "s1", Name: "alpha", Color: "#aaa", Status: "idle"},
		{ID: "s2", Name: "beta", Color: "#bbb", Status: "idle"},
	}
	out, _ := m.Update(listMsg(sessions))
	mm := out.(Model)
	if !mm.historyLoaded["s1"] || !mm.historyLoaded["s2"] {
		t.Fatalf("expected both sessions marked loaded, got %+v", mm.historyLoaded)
	}
}

// TestListMsg_DoesNotRefireForKnownSession ensures a session id we've
// already seen stays marked loaded across a second listMsg — the guard
// is the whole point of historyLoaded (otherwise the 2s refresh tick
// would re-fire the RPC every cycle).
func TestListMsg_DoesNotRefireForKnownSession(t *testing.T) {
	m := Model{
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{"s1": true},
		groupCollapsed: map[string]bool{},
	}
	sessions := []Session{
		{ID: "s1", Name: "alpha", Color: "#aaa", Status: "idle"},
	}
	out, _ := m.Update(listMsg(sessions))
	mm := out.(Model)
	// Still loaded, and crucially still only that one entry — no extra
	// keys added (a duplicate-fire bug would still be caught by the
	// daemon-side idempotency, but the client-side guard is the real
	// fix).
	if !mm.historyLoaded["s1"] {
		t.Fatalf("expected s1 still loaded")
	}
	if len(mm.historyLoaded) != 1 {
		t.Fatalf("expected exactly 1 loaded entry, got %d", len(mm.historyLoaded))
	}
}

// TestSessionIdResolved_TriggersReload checks that a session_id_resolved
// event marks the session loaded and returns a non-nil Cmd batch (we
// can't introspect the batch contents, but a nil cmd would mean we
// dropped the reload entirely).
func TestSessionIdResolved_TriggersReload(t *testing.T) {
	m := Model{
		conversation:  map[string][]Turn{},
		historyLoaded: map[string]bool{},
	}
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "session_id_resolved",
			"event_params": map[string]any{
				"session_id":        "s_new",
				"claude_session_id": "abc",
			},
		},
	})
	out, cmd := m.Update(ev)
	mm := out.(Model)
	if !mm.historyLoaded["s_new"] {
		t.Fatalf("expected s_new marked loaded after session_id_resolved")
	}
	if cmd == nil {
		t.Fatalf("expected non-nil Cmd batch (refresh + listen + loadHistory)")
	}
}
