// Package model — scroll.go: per-session scroll offset bookkeeping
// for the conversation viewport. Tracks per-session offsets in
// m.scrollOffset, recomputes max-offset against the wrapped line count
// reported by viewport_view.go, and provides scrollUp/scrollDown/
// scrollToTop/scrollToBottom for the active pane key handlers.
//
// recomputeViewportGeom is here too because the inner-width / inner-
// height it caches (m.lastViewportInnerW / H) feed both maxScrollFor
// and halfViewportPage — keeping them adjacent reads more naturally.
package model

import "strings"

// maxScrollFor reports the maximum scroll offset (in lines) for the
// given session, based on the most-recently-rendered viewport
// geometry. If the conversation fits entirely on screen, max is 0
// (nowhere to scroll). Recomputes the wrapped line count on demand
// from the cached innerW so callers don't depend on viewMain having
// run since the last conversation mutation.
func (m Model) maxScrollFor(sid string) int {
	if m.lastViewportInnerH <= 0 || m.lastViewportInnerW <= 0 {
		// We haven't rendered yet; keep the offset whatever it is so
		// the first render establishes geometry. Returning a huge
		// number would let scrollUp run away before render lands.
		return m.scrollOffset[sid]
	}
	turns := m.conversation[sid]
	if len(turns) == 0 {
		return 0
	}
	// "color" doesn't affect line count, only ANSI bytes — we can pass
	// any string. Color the focused session's color when available so
	// downstream styling is consistent if the test checks the rendered
	// output too.
	color := "12"
	for i := range m.sessions {
		if m.sessions[i].ID == sid {
			color = m.sessions[i].Color
			break
		}
	}
	body := renderTurns(turns, color, m.lastViewportInnerW)
	// renderTurns trailing-newlines each turn, so split on "\n".
	lc := strings.Count(body, "\n")
	// 2 rows of banner + spacer subtract from the visible body area;
	// matches the layout in renderViewport (header + "\n\n").
	visible := m.lastViewportInnerH - 2
	if visible < 1 {
		visible = 1
	}
	max := lc - visible
	if max < 0 {
		return 0
	}
	return max
}

// scrollUp moves the focused session's scroll position UP by n lines
// (toward older messages), clamped to maxScrollFor. No-op if no
// session is focused.
func (m *Model) scrollUp(n int) {
	sid := m.focusedSessionID()
	if sid == "" {
		return
	}
	max := m.maxScrollFor(sid)
	cur := m.scrollOffset[sid] + n
	if cur > max {
		cur = max
	}
	if cur < 0 {
		cur = 0
	}
	m.scrollOffset[sid] = cur
}

// scrollDown moves the focused session's scroll position DOWN by n
// lines (toward the latest message), clamped to 0. When the user
// arrives back at the bottom (offset==0), unread-count is cleared so
// the "↓ N new" indicator goes away.
func (m *Model) scrollDown(n int) {
	sid := m.focusedSessionID()
	if sid == "" {
		return
	}
	cur := m.scrollOffset[sid] - n
	if cur < 0 {
		cur = 0
	}
	m.scrollOffset[sid] = cur
	if cur == 0 {
		m.newSinceScroll[sid] = 0
	}
}

// scrollToTop pins the focused session's view to the oldest visible
// line (max offset). Bound to "home" / "g g".
func (m *Model) scrollToTop() {
	sid := m.focusedSessionID()
	if sid == "" {
		return
	}
	m.scrollOffset[sid] = m.maxScrollFor(sid)
}

// scrollToBottom pins the focused session's view to the latest line
// (offset 0) and clears the unread-count. Bound to "end" / "G".
func (m *Model) scrollToBottom() {
	sid := m.focusedSessionID()
	if sid == "" {
		return
	}
	m.scrollOffset[sid] = 0
	m.newSinceScroll[sid] = 0
}

// recomputeViewportGeom recalculates the conversation pane's inner
// width and height from the current m.width / m.height, mirroring the
// layout math in viewMain. We cache these on the model so the scroll
// helpers can answer "max offset" without re-running the layout —
// and so they work even before the first View() call lands. Called
// from WindowSizeMsg and from listMsg (in case the rail visibility
// changed before any resize arrived).
func (m *Model) recomputeViewportGeom() {
	leftW := 24
	composeH := 3
	h := m.height - composeH - 2 - 2
	if m.slashPopupVisible() {
		h -= len(m.slashPopupCmds)
	}
	if h < 5 {
		h = 5
	}
	railVisible := !m.railCollapsed
	editorVisible := m.editor.visible
	available := m.width
	if available < 1 {
		available = 1
	}
	var convoW int
	switch {
	case railVisible && editorVisible:
		rest := available - leftW - 2
		if rest < 40 {
			rest = 40
		}
		editorW := rest / 2
		if editorW < 20 {
			editorW = 20
		}
		convoW = rest - editorW
		if convoW < 20 {
			convoW = 20
		}
	case !railVisible && editorVisible:
		rest := available - 2
		editorW := rest / 2
		if editorW < 20 {
			editorW = 20
		}
		convoW = rest - editorW
		if convoW < 20 {
			convoW = 20
		}
	case railVisible && !editorVisible:
		convoW = available - leftW - 2
		if convoW < 20 {
			convoW = 20
		}
	default:
		convoW = available - 2
		if convoW < 20 {
			convoW = 20
		}
	}
	// renderViewport draws inside a rounded border (1 col each side),
	// so the inner width for wrapping is convoW - 2.
	m.lastViewportInnerW = convoW - 2
	m.lastViewportInnerH = h
}

// halfViewportPage returns the page-step for pgup/pgdown — half the
// viewport's visible inner height, with a sensible floor so we always
// move by at least a few lines even before the first render.
func (m Model) halfViewportPage() int {
	h := m.lastViewportInnerH / 2
	if h < 5 {
		h = 5
	}
	return h
}

// clampAllScrollOffsets re-applies maxScrollFor to every per-session
// offset. Called from tea.WindowSizeMsg so a terminal resize that
// shrinks the viewport doesn't leave stale offsets pointing past the
// new max.
func (m *Model) clampAllScrollOffsets() {
	for sid, off := range m.scrollOffset {
		max := m.maxScrollFor(sid)
		if off > max {
			m.scrollOffset[sid] = max
		}
		if off < 0 {
			m.scrollOffset[sid] = 0
		}
	}
}
