// Package model — viewport_view.go: right-pane conversation rendering.
// As of the embedded-PTY pivot, claude renders its own UI inside our
// rounded frame via a per-session vt.Emulator (see internal/ptypane).
// This file frames that pane and handles two edge cases: no focused
// session at all (empty placeholder) and a session whose pane hasn't
// been allocated yet (rare; one frame between listMsg and pane init).
package model

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/talmes1502/chubby/tui/internal/ptypane"
	"github.com/talmes1502/chubby/tui/internal/views"
)

// viewportRender is the result of renderViewport. The lineCount field
// is kept on the struct for back-compat with callers that read it,
// but it's now always 0 — the vt emulator owns scrollback, so the
// parent doesn't need a wrapped-line count to clamp scroll offsets.
type viewportRender struct {
	view      string
	lineCount int
}

// renderViewport draws the focused session's live PTY view inside a
// rounded border. Bubble Tea's renderer treats the output as
// scanlines and passes SGR escapes through verbatim — vt's Render()
// strips cursor-positioning escapes (they're absorbed into screen
// state), so the result composes with the parent frame without
// fighting.
//
// The signature still carries the old conversation/usage/timing
// arguments because callers were threaded through them; they're now
// ignored. Phase 4 will drop them once every call-site is updated.
func renderViewport(
	s *Session,
	conversation map[string][]Turn,
	w, h, spinnerFrame, scrollOffset, newCount int,
	active bool,
	usage sessionUsage,
	thinkingStartedAt /* unused */ interface{},
	generationStartedAt /* unused */ interface{},
	pane *ptypane.Pane,
) string {
	_ = conversation
	_ = spinnerFrame
	_ = scrollOffset
	_ = newCount
	_ = usage
	_ = thinkingStartedAt
	_ = generationStartedAt
	r := renderViewportFull(s, w, h, active, pane)
	return r.view
}

func renderViewportFull(
	s *Session,
	w, h int,
	active bool,
	pane *ptypane.Pane,
) viewportRender {
	borderColor := inactivePaneBorderColor
	if active {
		borderColor = activePaneBorderColor
	}
	if s == nil {
		dim := views.Dim
		bold := views.Bold
		body := bold.Render("no sessions yet") + "\n\n" +
			dim.Render("press") + " " +
			bold.Render("Ctrl+N") + " " +
			dim.Render("to create one") + "\n" +
			dim.Render("or") + " " +
			bold.Render("chubby spawn --name <n> --cwd <dir>") + " " +
			dim.Render("from another terminal")
		return viewportRender{
			view: lipgloss.NewStyle().Width(w).Height(h).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(borderColor).
				Padding(1, 2).
				Render(body),
		}
	}
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(w).Height(h)
	if s.Status == StatusDead {
		// Dead-session placeholder. Showing the captured-PTY ghost
		// (the screen claude was on when it died) is hostile UX —
		// the user wonders why nothing responds. Replace it with an
		// explicit "this session ended" message that names the
		// respawn key.
		dim := views.Dim
		bold := views.Bold
		body := bold.Render("session ended") + "\n\n" +
			dim.Render("press") + " " +
			bold.Render("Ctrl+P") + " " +
			dim.Render("to respawn") + " " +
			bold.Render(s.Name) + "\n" +
			dim.Render("with the same name and cwd")
		return viewportRender{
			view: frame.Padding(1, 2).Render(body),
		}
	}
	if pane == nil {
		// Briefly possible during session creation between listMsg
		// and pane init. Render an empty frame rather than dragging
		// in the parsed-Turn renderer for one frame's worth of UX.
		return viewportRender{
			view: frame.Padding(1, 2).Render(views.Dim.Render(
				"(connecting…)")),
		}
	}
	// Resize the pane to match the current frame inner area so claude
	// redraws to fit if the user has resized since the pane was
	// created. Cheap when dimensions haven't changed.
	pane.Resize(w-2, h-2)
	return viewportRender{view: frame.Render(pane.View())}
}

// renderTurns is a no-op stub kept so legacy scroll bookkeeping
// (maxScrollFor in scroll.go) compiles. The vt emulator owns
// scrollback now; the parsed-Turn rendering path is unreachable in
// the live UI. Removed entirely once scroll.go is rewired to vt
// (Phase 5 in docs/plans/2026-05-02-embedded-claude-pty.md).
func renderTurns(turns []Turn, sessionColor string, innerWidth int) string {
	_ = turns
	_ = sessionColor
	_ = innerWidth
	return ""
}

// stylePathMentions / pathStyle / pathRE are also retained for legacy
// callers (the editor pane's path harvester) — they don't depend on
// the parsed-Turn renderer being live, just on the regex and styling.

var pathStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("14")).
	Underline(true)

func stylePathMentions(s string) string {
	idx := pathRE.FindAllStringIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b []byte
	last := 0
	for _, ix := range idx {
		b = append(b, s[last:ix[0]]...)
		b = append(b, []byte(pathStyle.Render(s[ix[0]:ix[1]]))...)
		last = ix[1]
	}
	b = append(b, s[last:]...)
	return string(b)
}
