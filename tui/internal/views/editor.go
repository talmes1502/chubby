// Package views — editor.go: file-viewer pane rendered to the right of
// the conversation. Uses chroma for syntax highlighting (terminal256
// formatter, monokai style) so the pane reads well on a dark
// background. Scroll/title/lipgloss-frame composition lives here so the
// model doesn't have to know about chroma directly — it just hands us
// the path + raw content and gets back ANSI-styled lines.
package views

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

// HighlightFile returns ANSI-colored content + detected language. On
// any failure (unknown language, formatter error), returns the raw
// content and empty language so the editor pane still renders — just
// without color.
func HighlightFile(path, content string) (highlighted, lang string) {
	lex := lexers.Match(path)
	if lex == nil {
		return content, ""
	}
	lex = chroma.Coalesce(lex)
	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}
	fmtr := formatters.Get("terminal256")
	if fmtr == nil {
		fmtr = formatters.Fallback
	}
	iter, err := lex.Tokenise(nil, content)
	if err != nil {
		return content, ""
	}
	var b strings.Builder
	if err := fmtr.Format(&b, style, iter); err != nil {
		return content, ""
	}
	return b.String(), lex.Config().Name
}

// EditorPaneState is the minimal projection of the model's editor
// state that the renderer needs. Defined here (instead of taking the
// model.editorState directly) because views cannot import model — the
// model already imports views, so the dep would cycle.
type EditorPaneState struct {
	Path         string
	Highlighted  string
	Lang         string
	ScrollOffset int
	Err          error
	Truncated    bool // file was capped at the size limit
}

// RenderEditor draws the right-side editor pane. w/h are the outer
// dimensions including the rounded border; the content area is sized
// down accordingly. Lines beyond the visible window (after applying
// ScrollOffset) are dropped — scrolling is just an offset over the
// already-highlighted line slice.
func RenderEditor(es EditorPaneState, w, h int) string {
	if w < 20 {
		w = 20
	}
	if h < 5 {
		h = 5
	}
	title := truncatePath(es.Path, w-4)
	if es.Lang != "" {
		title += " · " + es.Lang
	}
	if es.Truncated {
		title += " (truncated)"
	}
	titleStyle := AccentBold
	dim := Dim

	// Body area: subtract 2 for the rounded border, 2 for the title row
	// + blank separator.
	bodyH := h - 4
	if bodyH < 1 {
		bodyH = 1
	}
	var body string
	if es.Err != nil {
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Render("error: " + es.Err.Error())
	} else {
		raw := es.Highlighted
		lines := strings.Split(raw, "\n")
		start := es.ScrollOffset
		if start > len(lines) {
			start = len(lines)
		}
		if start < 0 {
			start = 0
		}
		end := start + bodyH
		if end > len(lines) {
			end = len(lines)
		}
		if start < end {
			body = strings.Join(lines[start:end], "\n")
		}
		// Append a status line tail when there's room: "ln X-Y/Z"
		if start < end {
			tail := dim.Render(fmt.Sprintf("ln %d-%d/%d", start+1, end, len(lines)))
			body = body + "\n" + tail
		}
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(w - 2).
		Height(h - 2).
		Render(titleStyle.Render(title) + "\n\n" + body)
	return box
}

// truncatePath shortens a long absolute path to fit width, replacing
// the middle segments with an ellipsis. Keeps the leading slash and
// trailing basename so the user can still tell which file is open.
func truncatePath(p string, width int) string {
	if width <= 0 {
		return ""
	}
	if len([]rune(p)) <= width {
		return p
	}
	if width <= 3 {
		return string([]rune(p)[:width])
	}
	rs := []rune(p)
	keep := width - 1
	// Show roughly 1/3 prefix + ellipsis + 2/3 suffix so the basename
	// stays visible.
	prefix := keep / 3
	suffix := keep - prefix
	if prefix < 1 {
		prefix = 1
	}
	if suffix < 1 {
		suffix = 1
	}
	if prefix+suffix > len(rs) {
		return p
	}
	return string(rs[:prefix]) + "…" + string(rs[len(rs)-suffix:])
}
