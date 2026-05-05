package model

import (
	"testing"
	"time"

	"github.com/talmes1502/chubby/tui/internal/rpc"
)

// helper: build a session_usage_changed evMsg with the given fields.
// Mirrors the JSON-RPC envelope subscribe_events delivers, where the
// inner event is keyed by event_method/event_params.
func makeUsageEvent(sid string, in, out, cache int) evMsg {
	return evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "session_usage_changed",
			"event_params": map[string]any{
				"session_id":              sid,
				"input_tokens":            float64(in),
				"output_tokens":           float64(out),
				"cache_read_input_tokens": float64(cache),
				"ts":                      float64(time.Now().UnixMilli()),
			},
		},
	})
}

// TestUsageEvent_UpdatesLastUsage drives the reducer with a single
// session_usage_changed event and confirms the per-session totals
// land in m.lastUsage.
func TestUsageEvent_UpdatesLastUsage(t *testing.T) {
	m := New(&rpc.Client{})
	out, _ := m.Update(makeUsageEvent("s1", 1234, 56, 7890))
	mm := out.(Model)
	got, ok := mm.lastUsage["s1"]
	if !ok {
		t.Fatalf("expected lastUsage[s1] to exist")
	}
	if got.InputTokens != 1234 {
		t.Errorf("InputTokens = %d want 1234", got.InputTokens)
	}
	if got.OutputTokens != 56 {
		t.Errorf("OutputTokens = %d want 56", got.OutputTokens)
	}
	if got.CacheReadInputTokens != 7890 {
		t.Errorf("CacheReadInputTokens = %d want 7890", got.CacheReadInputTokens)
	}
	if got.LastUpdate.IsZero() {
		t.Errorf("LastUpdate should be set")
	}
}

// TestUsageEvent_PopulatesSamples confirms each session_usage_changed
// event pushes a sample into the ring, and the ring is bounded to 10
// entries even after a flood of events.
func TestUsageEvent_PopulatesSamples(t *testing.T) {
	m := New(&rpc.Client{})
	// Push 12 events into the same session — the model must keep the
	// 10 most recent and drop the rest.
	for i := 0; i < 12; i++ {
		out, _ := m.Update(makeUsageEvent("s1", 100, i*10, 0))
		m = out.(Model)
	}
	got := m.lastUsage["s1"].samples
	if len(got) != 10 {
		t.Fatalf("samples len = %d want 10 (ring cap)", len(got))
	}
	// The retained samples must be the LATEST 10 — first kept sample
	// has OutputTokens = 20 (i=2), last has 110 (i=11).
	if got[0].OutputTokens != 20 {
		t.Errorf("oldest retained sample OutputTokens = %d want 20", got[0].OutputTokens)
	}
	if got[len(got)-1].OutputTokens != 110 {
		t.Errorf("newest sample OutputTokens = %d want 110", got[len(got)-1].OutputTokens)
	}
}

// TestThinkingStatus_StartsAtFlipToThinking confirms a
// session_status_changed event with status=thinking populates
// thinkingStartedAt; flipping to any other status clears it.
func TestThinkingStatus_StartsAtFlipToThinking(t *testing.T) {
	m := New(&rpc.Client{})
	flip := func(sid, status string) evMsg {
		return evMsg(rpc.Event{
			Method: "event",
			Params: map[string]any{
				"event_method": "session_status_changed",
				"event_params": map[string]any{
					"session_id": sid,
					"status":     status,
				},
			},
		})
	}
	out, _ := m.Update(flip("s1", "thinking"))
	mm := out.(Model)
	if _, ok := mm.thinkingStartedAt["s1"]; !ok {
		t.Fatalf("thinkingStartedAt[s1] should be set after flip to thinking")
	}
	// Flip to idle clears it.
	out2, _ := mm.Update(flip("s1", "idle"))
	mm2 := out2.(Model)
	if _, ok := mm2.thinkingStartedAt["s1"]; ok {
		t.Fatalf("thinkingStartedAt[s1] should be cleared after flip away from thinking")
	}
}
