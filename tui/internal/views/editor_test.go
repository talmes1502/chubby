package views

import (
	"strings"
	"testing"
)

// TestHighlightFile_Python — ensure chroma actually highlights a Python
// snippet: the result should differ from the raw input (ANSI escapes
// inserted) and we should detect "Python" as the language.
func TestHighlightFile_Python(t *testing.T) {
	const code = "def foo(x):\n    return x + 1\n"
	hl, lang := HighlightFile("/tmp/foo.py", code)
	if lang == "" {
		t.Fatalf("expected non-empty lang for foo.py, got %q", lang)
	}
	if !strings.Contains(strings.ToLower(lang), "python") {
		t.Fatalf("expected lang to mention Python, got %q", lang)
	}
	if hl == code {
		t.Fatalf("highlighted output equals raw — chroma didn't run")
	}
	// terminal256 inserts ESC [ ... m sequences.
	if !strings.Contains(hl, "\x1b[") {
		t.Fatalf("expected ANSI escape sequences in highlighted output, got: %q", hl)
	}
}

// TestHighlightFile_UnknownExt — extension chroma can't match (.zzz)
// returns the raw content untouched and an empty language string.
// Verifies the "graceful fallback" contract.
func TestHighlightFile_UnknownExt(t *testing.T) {
	const code = "this is not really a programming language\n"
	hl, lang := HighlightFile("/tmp/foo.zzz", code)
	if lang != "" {
		t.Fatalf("expected empty lang for unknown extension, got %q", lang)
	}
	if hl != code {
		t.Fatalf("expected raw passthrough for unknown ext, got: %q", hl)
	}
}

// TestHighlightFile_Empty — empty file shouldn't blow up; we return
// either an empty highlighted string or a benign ANSI-reset prelude.
// Either way the language detection from the path should still work.
func TestHighlightFile_Empty(t *testing.T) {
	hl, lang := HighlightFile("/tmp/empty.py", "")
	// Lexer is matched by extension; even an empty file gets the Python
	// lang reported.
	if !strings.Contains(strings.ToLower(lang), "python") {
		t.Fatalf("expected python lang for empty.py, got %q", lang)
	}
	// Don't assert on hl content — chroma may emit terminal-init bytes
	// for an empty input. Just verify no panic.
	_ = hl
}

// TestRenderEditor_NoFileBody — renders something even when the
// highlighted body is empty (just-loaded zero-byte file).
func TestRenderEditor_EmptyBody(t *testing.T) {
	out := RenderEditor(EditorPaneState{
		Path:        "/tmp/foo.py",
		Highlighted: "",
		Lang:        "Python",
	}, 60, 20)
	if out == "" {
		t.Fatalf("expected non-empty render even for empty body")
	}
	if !strings.Contains(out, "Python") {
		t.Fatalf("expected lang in title, got: %q", out)
	}
}

// TestRenderEditor_ScrollOffset — start beyond line count should not
// panic and should render an empty body region.
func TestRenderEditor_ScrollOffsetClamped(t *testing.T) {
	hl := "line1\nline2\nline3\n"
	out := RenderEditor(EditorPaneState{
		Path:         "/tmp/foo.txt",
		Highlighted:  hl,
		ScrollOffset: 9999, // way past
	}, 60, 20)
	if out == "" {
		t.Fatalf("expected non-empty render")
	}
}

// TestRenderEditor_TruncatedNote — truncation flag surfaces in the
// title.
func TestRenderEditor_TruncatedTitle(t *testing.T) {
	out := RenderEditor(EditorPaneState{
		Path:        "/tmp/big.log",
		Highlighted: "x\n",
		Truncated:   true,
	}, 60, 20)
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected 'truncated' in title, got: %q", out)
	}
}

// TestTruncatePath_ShortPathPassthrough — paths that fit return as-is.
func TestTruncatePath_ShortPathPassthrough(t *testing.T) {
	in := "/foo/bar.py"
	got := truncatePath(in, 40)
	if got != in {
		t.Fatalf("expected passthrough, got %q", got)
	}
}

// TestTruncatePath_LongPathEllipsised — paths longer than width get
// shortened with an ellipsis but keep the basename.
func TestTruncatePath_LongPathEllipsised(t *testing.T) {
	in := "/very/long/path/to/some/deeply/nested/file_with_long_name.py"
	got := truncatePath(in, 30)
	if !strings.Contains(got, "…") {
		t.Fatalf("expected ellipsis in truncated path, got %q", got)
	}
	if !strings.Contains(got, "name.py") {
		t.Fatalf("expected basename to survive, got %q", got)
	}
}
