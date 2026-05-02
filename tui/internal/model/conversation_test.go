package model

import (
	"strings"
	"testing"
	"time"

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

// TestRenderViewport_RendersConnectingPlaceholder verifies the
// "(connecting…)" copy appears when a session is focused but no PTY
// pane has been allocated yet (one frame between listMsg and pane
// init). After the embedded-PTY pivot the parsed-Turn render path
// is gone — claude renders its own UI inside the frame via the
// per-session vt emulator. The "no messages yet" / "▸ user / pong
// assistant" assertions used to live here; they're moved to a Phase
// 5 follow-up that exercises pane.View() output.
func TestRenderViewport_RendersConnectingPlaceholder(t *testing.T) {
	s := &Session{ID: "s1", Name: "alpha", Color: "#abcdef"}
	out := renderViewport(s, map[string][]Turn{}, 60, 10, 0, 0, 0, true,
		sessionUsage{}, time.Time{}, time.Time{}, nil)
	if !strings.Contains(out, "connecting") {
		t.Fatalf("expected '(connecting…)' placeholder, got: %q", out)
	}
}
