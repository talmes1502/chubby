package views

import (
	"strings"
	"testing"
)

func TestRenderToolCall_BashHeader(t *testing.T) {
	out := RenderToolCall("Bash", "ls -la", "", false, 60)
	if !strings.Contains(out, "Bash command") {
		t.Fatalf("expected 'Bash command' header, got %q", out)
	}
	if !strings.Contains(out, "ls -la") {
		t.Fatalf("expected command body, got %q", out)
	}
	// Rounded border characters should appear.
	if !strings.Contains(out, "╭") || !strings.Contains(out, "╰") {
		t.Fatalf("expected rounded box border, got %q", out)
	}
}

func TestRenderToolCall_UnknownToolFallback(t *testing.T) {
	// A tool name we haven't mapped should still render — just without
	// the trailing verb word.
	out := RenderToolCall("CustomThing", "key=val", "", false, 60)
	if !strings.Contains(out, "CustomThing") {
		t.Fatalf("expected raw tool name in header, got %q", out)
	}
	if strings.Contains(out, "CustomThing command") {
		t.Fatalf("unknown tool should NOT get a 'command' suffix: %q", out)
	}
}

func TestRenderToolCall_LongSummaryTruncated(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := RenderToolCall("Bash", long, "", false, 30)
	if !strings.Contains(out, "…") {
		t.Fatalf("expected ellipsis on truncated summary, got %q", out)
	}
}

func TestRenderToolCall_WithResultPreview(t *testing.T) {
	out := RenderToolCall("Bash", "ls", "file1\nfile2", false, 60)
	if !strings.Contains(out, "file1") {
		t.Fatalf("expected result-preview line, got %q", out)
	}
}

func TestRenderToolCall_ErrorShowsCross(t *testing.T) {
	out := RenderToolCall("Edit", "/tmp/x.py",
		"User rejected this edit", true, 60)
	if !strings.Contains(out, "✗") {
		t.Fatalf("expected ✗ glyph for error result, got %q", out)
	}
	if !strings.Contains(out, "User rejected") {
		t.Fatalf("expected error preview text, got %q", out)
	}
}

func TestRenderToolCall_ErrorWithEmptyPreviewSaysRejected(t *testing.T) {
	// A rejection often arrives with an empty body — we still need a
	// human-visible signal, so the box shows "✗ rejected".
	out := RenderToolCall("Bash", "rm -rf /", "", true, 60)
	if !strings.Contains(out, "rejected") {
		t.Fatalf("expected fallback 'rejected' text, got %q", out)
	}
	if !strings.Contains(out, "✗") {
		t.Fatalf("expected ✗ glyph, got %q", out)
	}
}

func TestRenderToolCall_NoResultPreviewWhenEmpty(t *testing.T) {
	out := RenderToolCall("Bash", "ls", "", false, 60)
	// No third "  ..." line should appear; check by counting newlines —
	// header + body = 2 visible lines inside the box (plus 2 border lines).
	innerLines := strings.Count(out, "\n")
	if innerLines > 3 {
		t.Fatalf("expected ≤3 newlines without result preview, got %d in %q",
			innerLines, out)
	}
}
