package model

import (
	"testing"

	"github.com/USER/chubby/tui/internal/rpc"
)

// TestAppendTranscriptTurn_AppendsNew adds a turn to an empty conversation
// and verifies it lands. Baseline behavior — no dedup applies because
// there's nothing to dedup against.
func TestAppendTranscriptTurn_AppendsNew(t *testing.T) {
	m := Model{conversation: map[string][]Turn{}}
	m.appendTranscriptTurn("s1", "user", "hello", 100)
	turns := m.conversation["s1"]
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	if turns[0].Role != "user" || turns[0].Text != "hello" || turns[0].Ts != 100 {
		t.Fatalf("unexpected turn: %+v", turns[0])
	}
}

// TestAppendTranscriptTurn_DedupesExactDuplicate exercises the core
// motivation: a live event arrives matching the last seeded turn — it
// should be skipped, not appended.
func TestAppendTranscriptTurn_DedupesExactDuplicate(t *testing.T) {
	m := Model{conversation: map[string][]Turn{
		"s1": {{Role: "user", Text: "hello", Ts: 100}},
	}}
	m.appendTranscriptTurn("s1", "user", "hello", 200)
	turns := m.conversation["s1"]
	if len(turns) != 1 {
		t.Fatalf("expected dedup to skip, got %d turns", len(turns))
	}
	// Original ts preserved (we didn't overwrite, just skipped).
	if turns[0].Ts != 100 {
		t.Fatalf("expected original ts=100 preserved, got %d", turns[0].Ts)
	}
}

// TestAppendTranscriptTurn_DifferentTextAppends — same role, different
// text should NOT dedup (it's a new message).
func TestAppendTranscriptTurn_DifferentTextAppends(t *testing.T) {
	m := Model{conversation: map[string][]Turn{
		"s1": {{Role: "user", Text: "hello", Ts: 100}},
	}}
	m.appendTranscriptTurn("s1", "user", "world", 200)
	turns := m.conversation["s1"]
	if len(turns) != 2 {
		t.Fatalf("want 2 turns (different text), got %d", len(turns))
	}
	if turns[1].Text != "world" {
		t.Fatalf("expected second turn 'world', got %q", turns[1].Text)
	}
}

// TestAppendTranscriptTurn_DifferentRoleAppends — same text, different
// role should NOT dedup. (Edge case: an assistant echoing the user's
// last input verbatim is two distinct turns.)
func TestAppendTranscriptTurn_DifferentRoleAppends(t *testing.T) {
	m := Model{conversation: map[string][]Turn{
		"s1": {{Role: "user", Text: "hello", Ts: 100}},
	}}
	m.appendTranscriptTurn("s1", "assistant", "hello", 200)
	turns := m.conversation["s1"]
	if len(turns) != 2 {
		t.Fatalf("want 2 turns (different role), got %d", len(turns))
	}
}

// TestAppendTranscriptTurn_DedupWindowRespected — a duplicate of a turn
// that's INSIDE the last-5 window dedups; a duplicate of a turn OUTSIDE
// the window appends. This validates the window is a correctness
// boundary, not just a perf trick.
func TestAppendTranscriptTurn_DedupWindowRespected(t *testing.T) {
	// Seed with 6 distinct turns; "hello" is at index 0, outside the
	// last-5 window.
	m := Model{conversation: map[string][]Turn{
		"s1": {
			{Role: "user", Text: "hello", Ts: 1},
			{Role: "user", Text: "t1", Ts: 2},
			{Role: "user", Text: "t2", Ts: 3},
			{Role: "user", Text: "t3", Ts: 4},
			{Role: "user", Text: "t4", Ts: 5},
			{Role: "user", Text: "t5", Ts: 6},
		},
	}}
	m.appendTranscriptTurn("s1", "user", "hello", 7)
	turns := m.conversation["s1"]
	if len(turns) != 7 {
		t.Fatalf("expected hello to append (outside window), got %d turns", len(turns))
	}
	if turns[6].Text != "hello" || turns[6].Ts != 7 {
		t.Fatalf("expected appended hello at end, got %+v", turns[6])
	}

	// And: a duplicate of t5 (last entry, inside the window) should dedup.
	m2 := Model{conversation: map[string][]Turn{
		"s1": {
			{Role: "user", Text: "t1", Ts: 1},
			{Role: "user", Text: "t2", Ts: 2},
			{Role: "user", Text: "t3", Ts: 3},
			{Role: "user", Text: "t4", Ts: 4},
			{Role: "user", Text: "t5", Ts: 5},
		},
	}}
	m2.appendTranscriptTurn("s1", "user", "t5", 6)
	if got := len(m2.conversation["s1"]); got != 5 {
		t.Fatalf("expected dedup of t5, got %d turns", got)
	}
}

// TestAppendTranscriptTurn_RespectsTurnsCap — dedup is a separate
// concern from the per-session cap. A non-duplicate turn appended at
// the cap should still trim the oldest.
func TestAppendTranscriptTurn_RespectsTurnsCap(t *testing.T) {
	turns := make([]Turn, turnsCap)
	for i := range turns {
		turns[i] = Turn{Role: "assistant", Text: "old-" + string(rune('a'+i%26)), Ts: int64(i)}
	}
	m := Model{conversation: map[string][]Turn{"s": turns}}
	m.appendTranscriptTurn("s", "user", "newest-unique", 9999)
	got := m.conversation["s"]
	if len(got) != turnsCap {
		t.Fatalf("expected cap of %d, got %d", turnsCap, len(got))
	}
	if got[len(got)-1].Text != "newest-unique" {
		t.Fatalf("last turn should be 'newest-unique', got %q", got[len(got)-1].Text)
	}
}

// TestEvMsg_TranscriptMessage_DedupsAgainstLastTurn — wires the dedup
// path through the actual reducer to confirm the evMsg handler uses
// appendTranscriptTurn (not a stale duplicate codepath). Mirrors the
// real-world tailer race: history seed already lands, then the tailer
// replays the last turn as a live event.
func TestEvMsg_TranscriptMessage_DedupsAgainstLastTurn(t *testing.T) {
	m := Model{conversation: map[string][]Turn{
		"s_abc": {{Role: "user", Text: "hello world", Ts: 100}},
	}}
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "transcript_message",
			"event_params": map[string]any{
				"session_id": "s_abc",
				"role":       "user",
				"text":       "hello world",
				"ts":         float64(200),
			},
		},
	})
	out, _ := m.Update(ev)
	got := out.(Model).conversation["s_abc"]
	if len(got) != 1 {
		t.Fatalf("want 1 turn after dedup, got %d", len(got))
	}
	if got[0].Ts != 100 {
		t.Fatalf("expected original ts preserved, got %d", got[0].Ts)
	}
}
