package model

import (
	"testing"

	"github.com/USER/chubby/tui/internal/rpc"
)

// scrollTestModel returns a model with a focused session, a long
// pre-loaded conversation, and viewport geometry set so maxScrollFor
// is non-zero. Used by every scroll test.
func scrollTestModel() Model {
	turns := make([]Turn, 50)
	for i := range turns {
		turns[i] = Turn{Role: "user", Text: "msg-" + string(rune('a'+i%26)), Ts: int64(i)}
	}
	m := Model{
		sessions:           []Session{{ID: "s1", Name: "alpha", Color: "12"}},
		focused:            0,
		conversation:       map[string][]Turn{"s1": turns},
		scrollOffset:       map[string]int{},
		newSinceScroll:     map[string]int{},
		lastViewportInnerW: 60,
		lastViewportInnerH: 10,
	}
	return m
}

// TestScrollUp_IncrementsOffset — basic up-scroll moves the offset.
//
// Skipped after the embedded-PTY pivot: the parsed-Turn scroll
// helpers operate on a wrapped-line count from renderTurns(), which
// is now stubbed to "" because vt.Emulator owns scrollback. Phase 5
// of docs/plans/2026-05-02-embedded-claude-pty.md rewires PgUp/PgDn
// to vt.Scrollback.Scroll(); these tests get rewritten then.
func TestScrollUp_IncrementsOffset(t *testing.T) {
	t.Skip("scrollUp now no-ops; vt.Emulator owns scrollback. Phase 5 rewires.")
}

// TestScrollDown_DecrementsOffset — down-scroll reduces offset.
func TestScrollDown_DecrementsOffset(t *testing.T) {
	m := scrollTestModel()
	m.scrollOffset["s1"] = 5
	m.scrollDown(2)
	if m.scrollOffset["s1"] != 3 {
		t.Fatalf("expected offset=3, got %d", m.scrollOffset["s1"])
	}
}

// TestScrollDown_ClampsAtZero — scrolling below 0 stops at 0 and
// triggers the unread-clear.
func TestScrollDown_ClampsAtZero(t *testing.T) {
	m := scrollTestModel()
	m.scrollOffset["s1"] = 2
	m.newSinceScroll["s1"] = 5
	m.scrollDown(99)
	if m.scrollOffset["s1"] != 0 {
		t.Fatalf("expected offset clamped to 0, got %d", m.scrollOffset["s1"])
	}
	if m.newSinceScroll["s1"] != 0 {
		t.Fatalf("expected unread cleared on bottom, got %d", m.newSinceScroll["s1"])
	}
}

// TestScrollUp_ClampsAtMaxScroll — same Phase 5 deferral as
// TestScrollUp_IncrementsOffset.
func TestScrollUp_ClampsAtMaxScroll(t *testing.T) {
	t.Skip("scrollUp now no-ops; vt.Emulator owns scrollback. Phase 5 rewires.")
}

// TestEnd_ResetsToZeroAndClearsUnread — scrollToBottom both pins to
// the bottom and clears the unread counter.
func TestEnd_ResetsToZeroAndClearsUnread(t *testing.T) {
	m := scrollTestModel()
	m.scrollOffset["s1"] = 7
	m.newSinceScroll["s1"] = 3
	m.scrollToBottom()
	if m.scrollOffset["s1"] != 0 {
		t.Fatalf("expected scrollToBottom→offset=0, got %d", m.scrollOffset["s1"])
	}
	if m.newSinceScroll["s1"] != 0 {
		t.Fatalf("expected scrollToBottom→unread=0, got %d", m.newSinceScroll["s1"])
	}
}

// TestScrollToTop_PinsToMax — scrollToTop pins offset to the maximum
// (oldest line visible at the top of the viewport).
func TestScrollToTop_PinsToMax(t *testing.T) {
	m := scrollTestModel()
	want := m.maxScrollFor("s1")
	m.scrollToTop()
	if got := m.scrollOffset["s1"]; got != want {
		t.Fatalf("expected scrollToTop→offset=%d, got %d", want, got)
	}
}

// TestScrollHelpers_NoFocusedSession — every helper tolerates the
// "no session focused" case (m.focused points outside the slice).
func TestScrollHelpers_NoFocusedSession(t *testing.T) {
	m := Model{
		sessions:       []Session{},
		conversation:   map[string][]Turn{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	m.scrollUp(1)
	m.scrollDown(1)
	m.scrollToTop()
	m.scrollToBottom()
	// The maps stay empty; no panic.
	if len(m.scrollOffset) != 0 {
		t.Fatalf("expected no-op when no session focused, got %v", m.scrollOffset)
	}
}

// TestNewMessage_AtBottomDoesNotIncrementUnread — when the user is
// pinned to the bottom (offset==0), live transcript messages should
// NOT bump newSinceScroll. The user's eyes are already on the latest
// line; there's nothing "unread."
func TestNewMessage_AtBottomDoesNotIncrementUnread(t *testing.T) {
	m := Model{
		conversation:   map[string][]Turn{},
		scrollOffset:   map[string]int{},
		newSinceScroll: map[string]int{},
	}
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "transcript_message",
			"event_params": map[string]any{
				"session_id": "s1",
				"role":       "assistant",
				"text":       "hello",
				"ts":         float64(10),
			},
		},
	})
	out, _ := m.Update(ev)
	got := out.(Model)
	if got.newSinceScroll["s1"] != 0 {
		t.Fatalf("expected no unread bump while at bottom, got %d",
			got.newSinceScroll["s1"])
	}
}

// TestNewMessage_ScrolledUpIncrementsUnread — when scrolled up, a new
// non-duplicate transcript message should bump newSinceScroll.
func TestNewMessage_ScrolledUpIncrementsUnread(t *testing.T) {
	m := Model{
		conversation:   map[string][]Turn{},
		scrollOffset:   map[string]int{"s1": 5},
		newSinceScroll: map[string]int{},
	}
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "transcript_message",
			"event_params": map[string]any{
				"session_id": "s1",
				"role":       "assistant",
				"text":       "hello",
				"ts":         float64(10),
			},
		},
	})
	out, _ := m.Update(ev)
	got := out.(Model)
	if got.newSinceScroll["s1"] != 1 {
		t.Fatalf("expected unread=1 after one new turn, got %d",
			got.newSinceScroll["s1"])
	}
}

// TestNewMessage_DedupedDoesNotIncrementUnread — the dedup window
// covers the case where the daemon's tailer replays a turn we
// already have. A skipped (deduped) append must not bump the unread
// counter, otherwise the badge inflates with phantom messages on
// every re-tail.
func TestNewMessage_DedupedDoesNotIncrementUnread(t *testing.T) {
	m := Model{
		conversation: map[string][]Turn{
			"s1": {{Role: "user", Text: "hello", Ts: 1}},
		},
		scrollOffset:   map[string]int{"s1": 5},
		newSinceScroll: map[string]int{},
	}
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "transcript_message",
			"event_params": map[string]any{
				"session_id": "s1",
				"role":       "user",
				"text":       "hello", // duplicate
				"ts":         float64(10),
			},
		},
	})
	out, _ := m.Update(ev)
	got := out.(Model)
	if got.newSinceScroll["s1"] != 0 {
		t.Fatalf("expected no unread bump on dedup, got %d",
			got.newSinceScroll["s1"])
	}
}

// TestEnd_ClearsUnread — pressing End (scrollToBottom) must clear
// the per-session unread count so the badge disappears immediately.
func TestEnd_ClearsUnread(t *testing.T) {
	m := scrollTestModel()
	m.newSinceScroll["s1"] = 7
	m.scrollOffset["s1"] = 3
	m.scrollToBottom()
	if m.newSinceScroll["s1"] != 0 {
		t.Fatalf("expected unread=0 after End, got %d", m.newSinceScroll["s1"])
	}
}

// TestRenderViewport_ScrolledUp_ShowsBadge — when scrollOffset > 0
// (TestRenderViewport_ScrolledUp_ShowsBadge,
// TestRenderViewport_AtBottom_NoBadge,
// TestRenderViewport_ScrolledUp_BannerHint were removed when the
// conversation pane pivoted to embed claude's live PTY view —
// vt.Emulator owns scrollback and renders its own scroll affordances,
// so the chubby-side "↓ N new" badge and "scrolled up" banner hint
// are no longer emitted.)

// TestClampAllScrollOffsets_OnResize — a resize that shrinks the
// conversation to fit on screen (max=0) should clamp all
// per-session offsets back to 0.
func TestClampAllScrollOffsets_OnResize(t *testing.T) {
	m := Model{
		sessions:           []Session{{ID: "s1", Color: "12"}},
		conversation:       map[string][]Turn{"s1": {{Role: "user", Text: "hi"}}},
		scrollOffset:       map[string]int{"s1": 99},
		newSinceScroll:     map[string]int{},
		lastViewportInnerW: 60,
		lastViewportInnerH: 100, // tall — content fits, max=0
	}
	m.clampAllScrollOffsets()
	if m.scrollOffset["s1"] != 0 {
		t.Fatalf("expected offset clamped to 0 after resize, got %d",
			m.scrollOffset["s1"])
	}
}
