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

// TestSeenAwaiting_ClearedWhenSessionLeavesAwaitingUser: when a
// session that was previously awaiting_user transitions back to
// thinking (or any other state), drop its seen mark so the next
// entry into awaiting_user re-summons the ⚡ glyph.
func TestSeenAwaiting_ClearedWhenSessionLeavesAwaitingUser(t *testing.T) {
	m := New(&rpc.Client{})
	m.sessions = []Session{{ID: "s1", Name: "alpha"}}
	m.seenAwaiting = map[string]bool{"s1": true}
	flip := func(status string) evMsg {
		return evMsg(rpc.Event{
			Method: "event",
			Params: map[string]any{
				"event_method": "session_status_changed",
				"event_params": map[string]any{
					"session_id": "s1",
					"status":     status,
				},
			},
		})
	}
	out, _ := m.Update(flip("thinking"))
	mm := out.(Model)
	if _, ok := mm.seenAwaiting["s1"]; ok {
		t.Fatalf("seenAwaiting[s1] should be cleared on flip away from awaiting_user")
	}
}

// TestSeenAwaiting_NotMarkedWhenAwaitingArrivesOnNonFocusedSession:
// the ⚡ alert is the whole point of the badge — a status change
// to awaiting_user on a session the user isn't focused on must NOT
// mark seen, or the user would never see the alert for unread
// responses.
func TestSeenAwaiting_NotMarkedWhenAwaitingArrivesOnNonFocusedSession(t *testing.T) {
	m := New(&rpc.Client{})
	m.sessions = []Session{
		{ID: "s1", Name: "alpha"},
		{ID: "s2", Name: "beta"},
	}
	m.focused = 0 // user is on s1
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
	out, _ := m.Update(flip("s2", "awaiting_user"))
	mm := out.(Model)
	if mm.seenAwaiting["s2"] {
		t.Fatalf("seenAwaiting[s2] must stay unset — user is on s1, not s2")
	}
}

// TestSeenAwaiting_MarkedWhenAwaitingArrivesOnFocusedSession: the
// dual case — claude finished generating in the session the user
// is already looking at; no ⚡ needed, the response is already
// on screen.
func TestSeenAwaiting_MarkedWhenAwaitingArrivesOnFocusedSession(t *testing.T) {
	m := New(&rpc.Client{})
	m.sessions = []Session{{ID: "s1", Name: "alpha"}}
	m.focused = 0
	out, _ := m.Update(evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "session_status_changed",
			"event_params": map[string]any{
				"session_id": "s1",
				"status":     "awaiting_user",
			},
		},
	}))
	mm := out.(Model)
	if !mm.seenAwaiting["s1"] {
		t.Fatalf("seenAwaiting[s1] should be marked when awaiting arrives on the focused session")
	}
}

// TestSetFocused_MarksSessionSeenIfAwaiting: focusing a session
// that's already awaiting_user marks it seen, so the rail glyph
// drops back to ○.
func TestSetFocused_MarksSessionSeenIfAwaiting(t *testing.T) {
	m := New(&rpc.Client{})
	m.sessions = []Session{
		{ID: "s1", Name: "alpha"},
		{ID: "s2", Name: "beta", Status: StatusAwaitingUser},
	}
	m.focused = 0
	m.setFocused(1) // jump to s2 (awaiting)
	if !m.seenAwaiting["s2"] {
		t.Fatalf("setFocused on awaiting_user session should mark seen")
	}
}
