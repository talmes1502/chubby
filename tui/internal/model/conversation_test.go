package model

import (
	"strings"
	"testing"

	"github.com/USER/chubby/tui/internal/rpc"
)

// TestEvMsg_TranscriptMessage_AppendsTurn drives the reducer with a
// transcript_message event wrapped in the same envelope subscribe_events
// produces and asserts that m.conversation[sid] grew by one Turn.
func TestEvMsg_TranscriptMessage_AppendsTurn(t *testing.T) {
	m := Model{conversation: map[string][]Turn{}}
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "transcript_message",
			"event_params": map[string]any{
				"session_id": "s_abc",
				"role":       "user",
				"text":       "hello world",
				"ts":         float64(123),
			},
		},
	})
	out, _ := m.Update(ev)
	got := out.(Model).conversation["s_abc"]
	if len(got) != 1 {
		t.Fatalf("want 1 turn, got %d", len(got))
	}
	if got[0].Role != "user" || got[0].Text != "hello world" || got[0].Ts != 123 {
		t.Fatalf("unexpected turn: %+v", got[0])
	}
}

func TestEvMsg_TranscriptMessage_CapsAtTurnsCap(t *testing.T) {
	// Pre-load with turnsCap turns, then push one more — oldest should be dropped.
	turns := make([]Turn, turnsCap)
	for i := range turns {
		turns[i] = Turn{Role: "assistant", Text: "old", Ts: int64(i)}
	}
	m := Model{conversation: map[string][]Turn{"s": turns}}
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "transcript_message",
			"event_params": map[string]any{
				"session_id": "s",
				"role":       "user",
				"text":       "newest",
				"ts":         float64(9999),
			},
		},
	})
	out, _ := m.Update(ev)
	got := out.(Model).conversation["s"]
	if len(got) != turnsCap {
		t.Fatalf("expected cap of %d, got %d", turnsCap, len(got))
	}
	if got[len(got)-1].Text != "newest" {
		t.Fatalf("last turn should be 'newest', got %q", got[len(got)-1].Text)
	}
	// First turn should now be the *second* of the original (oldest dropped).
	if got[0].Ts != 1 {
		t.Fatalf("oldest turn should have ts=1 after trim, got %d", got[0].Ts)
	}
}

// TestRenderViewport_NoTurns_ShowsPlaceholder verifies the empty-state
// placeholder copy renders for a session with no transcript yet.
func TestRenderViewport_NoTurns_ShowsPlaceholder(t *testing.T) {
	s := &Session{ID: "s1", Name: "alpha", Color: "#abcdef"}
	out := renderViewport(s, map[string][]Turn{}, 60, 10, 0)
	if !strings.Contains(out, "no messages yet") {
		t.Fatalf("expected placeholder copy in output, got: %q", out)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("expected session header in output, got: %q", out)
	}
}

func TestRenderViewport_RendersTurns(t *testing.T) {
	s := &Session{ID: "s1", Name: "alpha", Color: "#abcdef"}
	conv := map[string][]Turn{
		"s1": {
			{Role: "user", Text: "ping", Ts: 1},
			{Role: "assistant", Text: "pong", Ts: 2},
		},
	}
	out := renderViewport(s, conv, 60, 20, 0)
	if !strings.Contains(out, "ping") {
		t.Fatalf("expected user text, got: %q", out)
	}
	if !strings.Contains(out, "pong") {
		t.Fatalf("expected assistant text, got: %q", out)
	}
	// User turns get the ▸ marker.
	if !strings.Contains(out, "▸") {
		t.Fatalf("expected user-turn arrow marker, got: %q", out)
	}
}
