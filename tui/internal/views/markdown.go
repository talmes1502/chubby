// Package views — markdown.go: glamour-based assistant-text renderer.
// Claude responses contain markdown (bold, links, lists, fenced code);
// without rendering they show up as raw `**foo**` and `[bar](url)` in
// the conversation pane, which is hard to read. We pipe assistant text
// through glamour so it ends up styled the same way Claude's own UI
// shows it.
//
// Renderers are cached per-width so we don't reconstruct on every
// repaint — glamour spins up a goldmark parser+renderer pipeline that
// is comparatively expensive.
package views

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

var (
	mdMu        sync.Mutex
	mdRenderers = map[int]*glamour.TermRenderer{}
)

// getMarkdownRenderer returns a glamour renderer configured for the
// given word-wrap width, reusing one when possible. Returns nil if
// glamour fails to construct (caller should fall back to plain text).
func getMarkdownRenderer(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	mdMu.Lock()
	defer mdMu.Unlock()
	if r, ok := mdRenderers[width]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	mdRenderers[width] = r
	return r
}

// RenderMarkdown formats markdown source for terminal display at the
// given wrap width. On any error or empty input it returns the input
// unchanged so the caller can plug it in unconditionally.
//
// Glamour appends a trailing newline and sometimes wraps the whole
// block in extra leading blank lines; we trim both so the rendered
// output drops cleanly into existing layout.
func RenderMarkdown(src string, width int) string {
	if strings.TrimSpace(src) == "" {
		return src
	}
	r := getMarkdownRenderer(width)
	if r == nil {
		return src
	}
	out, err := r.Render(src)
	if err != nil {
		return src
	}
	return strings.Trim(out, "\n")
}
