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
//
// The style (chubbyMarkdownStyle below) is hand-tuned to match Claude
// Code's terminal aesthetic: subtle cyan/white accents on a dark
// background with no red anywhere. Glamour's bundled "dark" preset
// uses red highlights for inline code, bold, and headers, which is
// loud and reads as "warning" rather than "Claude's UI".
package views

import (
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
)

// blankRunRE matches three or more consecutive newlines. Glamour's
// default block layout emits these around lists and headers; Claude's
// own UI is denser, so we collapse them down to a single blank line.
var blankRunRE = regexp.MustCompile(`\n{3,}`)

var (
	mdMu        sync.Mutex
	mdRenderers = map[int]*glamour.TermRenderer{}
)

// sptr/bptr/uptr return pointers to their argument. Glamour's
// StylePrimitive uses pointer fields to distinguish "set to false" from
// "unset" — so we need addressable copies for every styled attribute.
func sptr(s string) *string { return &s }
func bptr(b bool) *bool     { return &b }
func uptr(u uint) *uint     { return &u }

// chubbyMarkdownStyle mimics Claude Code's terminal palette. Anchored
// on a soft cyan accent (color 81 = #5fafff in xterm-256) with white
// for emphasis and dim gray for low-priority text. Inline code uses a
// faint dark-grey backdrop instead of glamour-dark's loud red.
var chubbyMarkdownStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			BlockPrefix: "",
			BlockSuffix: "",
		},
		Margin: uptr(0),
	},
	BlockQuote: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  sptr("245"),
			Italic: bptr(true),
		},
		Indent:      uptr(2),
		IndentToken: sptr("│ "),
	},
	Paragraph: ansi.StyleBlock{},
	List: ansi.StyleList{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{},
		},
		LevelIndent: 2,
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			BlockSuffix: "\n",
			Color:       sptr("81"),
			Bold:        bptr(true),
		},
	},
	H1: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "# ",
			Color:  sptr("81"),
			Bold:   bptr(true),
		},
	},
	H2: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "## ",
			Color:  sptr("81"),
			Bold:   bptr(true),
		},
	},
	H3: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "### ",
			Color:  sptr("81"),
			Bold:   bptr(true),
		},
	},
	H4: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "#### ",
			Color:  sptr("81"),
			Bold:   bptr(true),
		},
	},
	H5: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "##### ",
			Color:  sptr("81"),
		},
	},
	H6: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "###### ",
			Color:  sptr("245"),
		},
	},
	Strikethrough: ansi.StylePrimitive{
		CrossedOut: bptr(true),
	},
	Emph: ansi.StylePrimitive{
		Italic: bptr(true),
	},
	Strong: ansi.StylePrimitive{
		Bold: bptr(true),
	},
	HorizontalRule: ansi.StylePrimitive{
		Color:  sptr("240"),
		Format: "\n--------\n",
	},
	Item: ansi.StylePrimitive{
		BlockPrefix: "• ",
	},
	Enumeration: ansi.StylePrimitive{
		BlockPrefix: ". ",
	},
	Task: ansi.StyleTask{
		StylePrimitive: ansi.StylePrimitive{},
		Ticked:         "[✓] ",
		Unticked:       "[ ] ",
	},
	Link: ansi.StylePrimitive{
		Color:     sptr("81"),
		Underline: bptr(true),
	},
	LinkText: ansi.StylePrimitive{
		Color: sptr("81"),
		Bold:  bptr(true),
	},
	Image: ansi.StylePrimitive{
		Color:     sptr("81"),
		Underline: bptr(true),
	},
	ImageText: ansi.StylePrimitive{
		Color:  sptr("81"),
		Format: "Image: {{.text}} →",
	},
	Code: ansi.StyleBlock{
		// Inline code — keep the visual cue subtle. Glamour-dark's red
		// background was the loudest mismatch with Claude's UI.
		StylePrimitive: ansi.StylePrimitive{
			Prefix:          " ",
			Suffix:          " ",
			Color:           sptr("117"),
			BackgroundColor: sptr("236"),
		},
	},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: sptr("250"),
			},
			Margin: uptr(2),
		},
		Chroma: &ansi.Chroma{
			Text:                ansi.StylePrimitive{Color: sptr("250")},
			Error:               ansi.StylePrimitive{Color: sptr("203")},
			Comment:             ansi.StylePrimitive{Color: sptr("244")},
			CommentPreproc:      ansi.StylePrimitive{Color: sptr("81")},
			Keyword:             ansi.StylePrimitive{Color: sptr("141")},
			KeywordReserved:     ansi.StylePrimitive{Color: sptr("141")},
			KeywordNamespace:    ansi.StylePrimitive{Color: sptr("141")},
			KeywordType:         ansi.StylePrimitive{Color: sptr("117")},
			Operator:            ansi.StylePrimitive{Color: sptr("250")},
			Punctuation:         ansi.StylePrimitive{Color: sptr("250")},
			Name:                ansi.StylePrimitive{Color: sptr("250")},
			NameBuiltin:         ansi.StylePrimitive{Color: sptr("117")},
			NameTag:             ansi.StylePrimitive{Color: sptr("141")},
			NameAttribute:       ansi.StylePrimitive{Color: sptr("117")},
			NameClass:           ansi.StylePrimitive{Color: sptr("117"), Bold: bptr(true)},
			NameConstant:        ansi.StylePrimitive{Color: sptr("117")},
			NameDecorator:       ansi.StylePrimitive{Color: sptr("81")},
			NameFunction:        ansi.StylePrimitive{Color: sptr("117")},
			LiteralNumber:       ansi.StylePrimitive{Color: sptr("215")},
			LiteralString:       ansi.StylePrimitive{Color: sptr("114")},
			LiteralStringEscape: ansi.StylePrimitive{Color: sptr("141")},
			GenericDeleted:      ansi.StylePrimitive{Color: sptr("203")},
			GenericEmph:         ansi.StylePrimitive{Italic: bptr(true)},
			GenericInserted:     ansi.StylePrimitive{Color: sptr("114")},
			GenericStrong:       ansi.StylePrimitive{Bold: bptr(true)},
			GenericSubheading:   ansi.StylePrimitive{Color: sptr("81")},
			Background:          ansi.StylePrimitive{BackgroundColor: sptr("236")},
		},
	},
	Table: ansi.StyleTable{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{},
		},
		CenterSeparator: sptr("┼"),
		ColumnSeparator: sptr("│"),
		RowSeparator:    sptr("─"),
	},
	DefinitionList: ansi.StyleBlock{},
	DefinitionTerm: ansi.StylePrimitive{},
	DefinitionDescription: ansi.StylePrimitive{
		BlockPrefix: "\n  ",
	},
	HTMLBlock: ansi.StyleBlock{},
	HTMLSpan:  ansi.StyleBlock{},
}

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
		glamour.WithStyles(chubbyMarkdownStyle),
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
	out = strings.Trim(out, "\n")
	// Collapse glamour's wider default block spacing to match Claude's
	// denser layout (single blank line between blocks, never more).
	return blankRunRE.ReplaceAllString(out, "\n\n")
}
