// Package model — viewport_view.go: right-pane conversation rendering.
// Frames the structured transcript inside a rounded border, slices to
// the visible window for scroll, and overlays a "↓ N new" badge when
// the user is scrolled up. Banner rendering lives in banner_view.go;
// markdown styling lives in views/markdown.go.
package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/USER/chubby/tui/internal/views"
)

// viewportRender is the result of renderViewport: the framed string to
// place into the layout, plus the wrapped-line count of the rendered
// transcript body (excluding the banner) — viewMain stashes the line
// count into m.lastViewportLineCount so the scroll-clamping helpers
// can answer "how far up?" without re-running the wrap.
type viewportRender struct {
	view      string
	lineCount int
}

// renderViewport draws the focused session's structured conversation:
// a colored session banner, then user prompts marked with a coloured
// arrow and assistant responses in the default fg, separated by blank
// lines. The previous implementation rendered the raw PTY byte stream,
// which was unreadable inside lipgloss because Claude's cursor-
// positioning escapes don't compose with lipgloss frames.
//
// scrollOffset slides the visible window UP by that many lines (0 =
// pinned to bottom — the historical behavior). newCount, when > 0 and
// scrollOffset > 0, renders a yellow "↓ N new messages · End to jump"
// badge bottom-right inside the frame; the banner also gets a
// "scrolled up" hint so the state is obvious without looking at the
// badge.
func renderViewport(
	s *Session,
	conversation map[string][]Turn,
	w, h, spinnerFrame, scrollOffset, newCount int,
	active bool,
	usage sessionUsage,
	thinkingStartedAt time.Time,
	generationStartedAt time.Time,
) string {
	r := renderViewportFull(s, conversation, w, h, spinnerFrame,
		scrollOffset, newCount, active, usage, thinkingStartedAt,
		generationStartedAt)
	return r.view
}

func renderViewportFull(
	s *Session,
	conversation map[string][]Turn,
	w, h, spinnerFrame, scrollOffset, newCount int,
	active bool,
	usage sessionUsage,
	thinkingStartedAt time.Time,
	generationStartedAt time.Time,
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
	isThinking := s.Status == StatusThinking
	header := renderSessionBanner(s, spinnerFrame, scrollOffset > 0,
		usage, thinkingStartedAt, generationStartedAt, isThinking)
	// Banner may now span two lines (activity row). Count its actual
	// rendered height so visibleH below subtracts the right amount.
	bannerLines := strings.Count(header, "\n") + 1
	if s.Status == StatusDead {
		hint := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true).
			Render("session is dead — press Ctrl+P to respawn")
		body := header + "\n\n" + hint
		turns := conversation[s.ID]
		if len(turns) > 0 {
			// Still show the prior transcript below the hint so the user
			// can see what happened before the session died.
			body += "\n\n" + renderTurns(turns, s.Color, w-2)
		}
		return viewportRender{view: frame.Render(body)}
	}
	turns := conversation[s.ID]
	if len(turns) == 0 {
		body := header + "\n\n" +
			views.Dim.
				Render("(no messages yet — type below to send)")
		return viewportRender{view: frame.Render(body)}
	}
	turnsBody := renderTurns(turns, s.Color, w-2)
	// Slice the rendered transcript by scrollOffset. The line count
	// we report excludes the banner+spacer, so callers can clamp
	// scrollOffset against just the content.
	lines := strings.Split(strings.TrimRight(turnsBody, "\n"), "\n")
	lineCount := len(lines)
	// Visible body area: total inner height minus banner + spacer.
	// (-2 for the rounded border was already applied via frame.Width.)
	// banner height is dynamic now (1 row when no usage / not thinking,
	// 2 rows when the activity line is present).
	visibleH := h - 2 - bannerLines - 1 // border (top+bottom) + banner rows + spacer
	if visibleH < 1 {
		visibleH = 1
	}
	end := lineCount - scrollOffset
	if end < 0 {
		end = 0
	}
	if end > lineCount {
		end = lineCount
	}
	start := end - visibleH
	if start < 0 {
		start = 0
	}
	visible := strings.Join(lines[start:end], "\n")
	body := header + "\n\n" + visible

	// "↓ N new" badge — only when actually scrolled away from the
	// bottom AND there are new turns the user hasn't seen yet.
	if scrollOffset > 0 && newCount > 0 {
		badge := views.Warn.
			Render(fmt.Sprintf("↓ %d new · End to jump", newCount))
		// Right-align the badge inside the inner width.
		body += "\n" + lipgloss.NewStyle().Width(w-2).
			Align(lipgloss.Right).Render(badge)
	}
	return viewportRender{view: frame.Render(body), lineCount: lineCount}
}

// renderTurns formats the structured transcript: user prompts marked
// with a coloured arrow, assistant responses through glamour for
// markdown styling, separated by blank lines. Pass innerWidth to fit
// inside a bordered frame (subtract 2 for the rounded border).
//
// File-path mentions in user prompts get a cyan-underline accent so
// the user can spot the things Ctrl+] would jump to.
func renderTurns(turns []Turn, sessionColor string, innerWidth int) string {
	if innerWidth < 10 {
		innerWidth = 10
	}
	userStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(sessionColor)).Bold(true).
		Width(innerWidth)
	var b strings.Builder
	for i, t := range turns {
		if i > 0 {
			b.WriteString("\n")
		}
		switch t.Role {
		case RoleUser:
			// User prompts are typed into the compose bar — usually plain
			// text, sometimes paths. Keep the path-mention accent and
			// skip glamour (running plain prose through it adds noise).
			b.WriteString(userStyle.Render("▸ " + stylePathMentions(t.Text)))
		default:
			// Claude's responses are markdown. Rendering them via glamour
			// turns **bold**, [links](url), bullet lists, and fenced code
			// into terminal-styled output that matches Claude's own UI.
			if t.Text != "" {
				b.WriteString(views.RenderMarkdown(t.Text, innerWidth))
			}
			// Tool calls render as rounded cyan boxes underneath the
			// prose so the eye reads "Claude said X, then ran Y".
			for j, tc := range t.Tools {
				if t.Text != "" || j > 0 {
					b.WriteString("\n")
				}
				b.WriteString(views.RenderToolCall(
					tc.Name, tc.Summary, tc.ResultPreview,
					tc.ResultIsError, innerWidth))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// pathStyle highlights inline path mentions: cyan + underline so the
// reader can spot a Ctrl+] target without it overpowering the prose.
var pathStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("14")).
	Underline(true)

// stylePathMentions wraps every regex-matched path in pathStyle. We
// run this on the plain text before lipgloss layout so the styling
// composes with the user/assistant outer style. Non-matching segments
// pass through unchanged.
func stylePathMentions(s string) string {
	idx := pathRE.FindAllStringIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, ix := range idx {
		b.WriteString(s[last:ix[0]])
		b.WriteString(pathStyle.Render(s[ix[0]:ix[1]]))
		last = ix[1]
	}
	b.WriteString(s[last:])
	return b.String()
}
