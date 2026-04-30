// Package views — toolbox.go: rounded-rectangle widget for tool calls
// in the conversation pane. Mirrors the look of Claude Code's CLI:
// a cyan-tinted rounded box with the tool name on the first line and
// the canonical arg (Bash → command, Read → file_path, …) on the
// second. When a result has been spliced in, a third dim line shows
// the first ~3 lines of stdout.
package views

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// toolBoxBorderColor is the soft cyan used for the box outline so it
// reads as "tool widget" without competing with prose text or the
// session-color accents. Same hue as glamour links/headers in
// chubbyMarkdownStyle (color "81" / #5fafff).
var toolBoxBorderColor = lipgloss.Color("81")

// toolBoxErrorBorderColor is the muted-red used for tool-result errors
// (rejections, runtime failures) so the user can spot a problem from a
// rail-flick distance.
var toolBoxErrorBorderColor = lipgloss.Color("203")

// toolHeaderStyle renders the first line of the box: "<Tool> <verb>".
var toolHeaderStyle = lipgloss.NewStyle().
	Foreground(toolBoxBorderColor).
	Bold(true)

// toolBodyStyle renders the canonical-arg line. Slightly dim so the
// box header reads as the primary affordance and the body as
// supporting detail.
var toolBodyStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("250"))

// toolResultStyle renders the spliced-in result preview. Even dimmer
// than the body so the eye lands on the call first; the result is
// "did it work" context.
var toolResultStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("244")).
	Italic(true)

// toolResultErrorStyle renders error/rejected result previews. Same
// muted red as the box border so the box reads as one cohesive
// "something went wrong" widget.
var toolResultErrorStyle = lipgloss.NewStyle().
	Foreground(toolBoxErrorBorderColor).
	Bold(true)

// toolHeaderWord maps a tool name to the word that follows it on the
// header line, mirroring Claude's UI conventions ("Bash command",
// "Read file", "Edit file", etc.). Tools without an entry show just
// the bare name.
var toolHeaderWord = map[string]string{
	"Bash":         "command",
	"BashOutput":   "output",
	"Read":         "file",
	"Edit":         "file",
	"Write":        "file",
	"MultiEdit":    "file",
	"NotebookEdit": "notebook",
	"Grep":         "search",
	"Glob":         "search",
	"WebFetch":     "fetch",
	"WebSearch":    "search",
	"TodoWrite":    "todo list",
	"Task":         "task",
}

// RenderToolCall renders one tool-call box. width is the available
// inner width (the conversation pane minus borders); the box wraps to
// fit. Returns a multi-line string with NO trailing newline so the
// caller can JoinVertical with surrounding content.
//
// When isError is true, the border and result text turn muted-red and
// the result preview gets a leading ✗ glyph — same visual rule Claude's
// own UI uses for rejected/failed tool calls.
func RenderToolCall(name, summary, resultPreview string, isError bool, width int) string {
	if width < 12 {
		width = 12
	}
	verb := toolHeaderWord[name]
	header := name
	if verb != "" {
		header = name + " " + verb
	}
	body := strings.TrimRight(summary, "\n")
	// Truncate over-long single-line summaries so the box doesn't
	// dominate the viewport. Multi-line summaries (rare) flow naturally.
	maxBodyW := width - 4
	if maxBodyW < 8 {
		maxBodyW = 8
	}
	if !strings.Contains(body, "\n") && lipgloss.Width(body) > maxBodyW {
		body = truncateWithEllipsis(body, maxBodyW)
	}
	lines := []string{toolHeaderStyle.Render(header)}
	if body != "" {
		lines = append(lines, toolBodyStyle.Render("  "+body))
	}
	resultStyle := toolResultStyle
	resultPrefix := "  "
	if isError {
		resultStyle = toolResultErrorStyle
		resultPrefix = "  ✗ "
	}
	rp := strings.TrimRight(resultPreview, "\n")
	if rp == "" && isError {
		// An empty error body still warrants a single line so the box
		// doesn't read as "succeeded" silently.
		rp = "rejected"
	}
	if rp != "" {
		// Indent each line of the result preview so it aligns under
		// the body. Cap to first 3 lines (the daemon already trims,
		// but defend at the boundary).
		rpLines := strings.Split(rp, "\n")
		if len(rpLines) > 3 {
			rpLines = append(rpLines[:3], "…")
		}
		for i, ln := range rpLines {
			if lipgloss.Width(ln) > maxBodyW {
				ln = truncateWithEllipsis(ln, maxBodyW)
			}
			// Only the first line gets the ✗; subsequent lines keep
			// the plain indent so the glyph isn't repeated in stack-
			// trace style previews.
			prefix := "  "
			if i == 0 {
				prefix = resultPrefix
			}
			lines = append(lines, resultStyle.Render(prefix+ln))
		}
	}
	border := toolBoxBorderColor
	if isError {
		border = toolBoxErrorBorderColor
	}
	content := strings.Join(lines, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Render(content)
}
