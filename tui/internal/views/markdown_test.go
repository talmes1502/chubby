package views

import (
	"strings"
	"testing"
)

func TestRenderMarkdown_BoldAndLink(t *testing.T) {
	got := RenderMarkdown("**hello** [world](https://example.com)", 80)
	// Glamour emits ANSI escapes for bold and link styling. We don't pin
	// the exact bytes (those depend on terminal-profile detection), but
	// the substring "hello" must survive and the raw ** markers must
	// not. Same for the link target text — "world" stays, "[world]" goes.
	if !strings.Contains(got, "hello") {
		t.Fatalf("missing 'hello' after render: %q", got)
	}
	if strings.Contains(got, "**hello**") {
		t.Fatalf("bold markers should be consumed: %q", got)
	}
	if strings.Contains(got, "[world]") {
		t.Fatalf("link brackets should be consumed: %q", got)
	}
}

func TestRenderMarkdown_PassesThroughEmpty(t *testing.T) {
	if got := RenderMarkdown("", 80); got != "" {
		t.Fatalf("empty input should pass through, got %q", got)
	}
	if got := RenderMarkdown("   ", 80); got != "   " {
		t.Fatalf("whitespace-only input should pass through, got %q", got)
	}
}

func TestRenderMarkdown_FallbackOnNarrowWidth(t *testing.T) {
	// width<20 is clamped; the call must not panic and must return some
	// rendered version of the input.
	got := RenderMarkdown("**bold**", 5)
	if !strings.Contains(got, "bold") {
		t.Fatalf("narrow width lost text: %q", got)
	}
}

func TestRenderMarkdown_RendererCacheReused(t *testing.T) {
	// Two calls at the same width should hit the same cached renderer.
	r1 := getMarkdownRenderer(60)
	r2 := getMarkdownRenderer(60)
	if r1 == nil || r1 != r2 {
		t.Fatalf("renderer should be cached per width: r1=%p r2=%p", r1, r2)
	}
}

// TestRenderMarkdown_FencedCodeBlock — chroma rejects bare ANSI-256
// color codes ("141") with "unknown style element"; this caused a
// runtime panic the first time a code block hit our custom theme.
// Exercise the path so future palette regressions surface in tests
// rather than at chubby-tui startup.
func TestRenderMarkdown_FencedCodeBlock(t *testing.T) {
	src := "Here:\n\n```python\ndef hello():\n    return 'world'\n```\n"
	got := RenderMarkdown(src, 80)
	if !strings.Contains(got, "hello") {
		t.Fatalf("expected fenced code body in output, got %q", got)
	}
}

func TestRenderMarkdown_CollapsesBlankRuns(t *testing.T) {
	// A list followed by a paragraph used to ship 3+ consecutive newlines
	// in glamour's default layout. The post-process should keep them at
	// most 2 (= one blank line) so the output reads as densely as
	// Claude's own UI.
	got := RenderMarkdown("- one\n- two\n\nparagraph", 80)
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("expected blank runs to be collapsed to \\n\\n, got %q", got)
	}
}
