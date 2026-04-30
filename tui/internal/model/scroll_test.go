package model

import (
	"strings"
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
func TestScrollUp_IncrementsOffset(t *testing.T) {
	m := scrollTestModel()
	m.scrollUp(3)
	if m.scrollOffset["s1"] != 3 {
		t.Fatalf("expected offset=3, got %d", m.scrollOffset["s1"])
	}
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

// TestScrollUp_ClampsAtMaxScroll — scrolling past max stops at max.
// max is computed from line count - visibleH; we don't pin an exact
// number (lipgloss wrapping is hard to predict line-for-line) but we
// verify scroll didn't blow past whatever maxScrollFor reports.
func TestScrollUp_ClampsAtMaxScroll(t *testing.T) {
	m := scrollTestModel()
	want := m.maxScrollFor("s1")
	if want <= 0 {
		t.Fatalf("test setup: expected non-zero max, got %d (maybe geom is wrong)", want)
	}
	m.scrollUp(99999)
	if got := m.scrollOffset["s1"]; got != want {
		t.Fatalf("expected offset clamped to maxScrollFor=%d, got %d", want, got)
	}
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
// AND newCount > 0, the rendered output should contain the "↓ N new"
// badge text. We don't check exact placement (lipgloss wrapping is
// painful in tests) but the substring must be present.
func TestRenderViewport_ScrolledUp_ShowsBadge(t *testing.T) {
	s := &Session{ID: "s1", Name: "alpha", Color: "12"}
	turns := make([]Turn, 30)
	for i := range turns {
		turns[i] = Turn{Role: "assistant", Text: "line" + string(rune('a'+i%26)), Ts: int64(i)}
	}
	conv := map[string][]Turn{"s1": turns}
	out := renderViewport(s, conv, 60, 10, 0, 5, 3, true)
	if !strings.Contains(out, "3 new") {
		t.Fatalf("expected badge with new count in output, got: %q", out)
	}
}

// TestRenderViewport_AtBottom_NoBadge — at the bottom (offset==0)
// the badge must NOT render even if newCount > 0 (defensive: if the
// reducer has a bug and leaves a stale newSinceScroll, the user
// shouldn't see a confusing "↓ N new" while looking at the latest).
func TestRenderViewport_AtBottom_NoBadge(t *testing.T) {
	s := &Session{ID: "s1", Name: "alpha", Color: "12"}
	turns := []Turn{{Role: "user", Text: "hi", Ts: 1}}
	conv := map[string][]Turn{"s1": turns}
	out := renderViewport(s, conv, 60, 10, 0, 0, 5, true)
	if strings.Contains(out, "new · End to jump") {
		t.Fatalf("did not expect badge when offset=0, got: %q", out)
	}
}

// TestRenderViewport_ScrolledUp_BannerHint — the banner gains a
// "scrolled up" hint when offset > 0, regardless of newCount, so the
// state is obvious even when no new messages have arrived yet.
func TestRenderViewport_ScrolledUp_BannerHint(t *testing.T) {
	s := &Session{ID: "s1", Name: "alpha", Color: "12"}
	turns := make([]Turn, 30)
	for i := range turns {
		turns[i] = Turn{Role: "assistant", Text: "line", Ts: int64(i)}
	}
	conv := map[string][]Turn{"s1": turns}
	out := renderViewport(s, conv, 60, 10, 0, 3, 0, true)
	if !strings.Contains(out, "scrolled up") {
		t.Fatalf("expected 'scrolled up' banner hint, got: %q", out)
	}
}

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
