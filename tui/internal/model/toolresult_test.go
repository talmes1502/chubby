package model

import (
	"testing"
)

// TestApplyToolResult — splicing a result preview into the matching
// prior tool call updates the in-place ToolCall.ResultPreview.
func TestApplyToolResult(t *testing.T) {
	m := &Model{
		conversation: map[string][]Turn{
			"s1": {
				{Role: "user", Text: "list files"},
				{Role: "assistant", Tools: []ToolCall{
					{ID: "tu1", Name: "Bash", Summary: "ls"},
				}},
			},
		},
	}
	m.applyToolResult("s1", "tu1", "file1\nfile2", false)
	got := m.conversation["s1"][1].Tools[0].ResultPreview
	if got != "file1\nfile2" {
		t.Fatalf("expected result preview to be spliced in, got %q", got)
	}
}

// TestApplyToolResult_NoMatch — splicing with an unknown id is a no-op
// (silent — we don't want to introduce a phantom ToolCall just because
// a stray result arrived).
func TestApplyToolResult_NoMatch(t *testing.T) {
	m := &Model{
		conversation: map[string][]Turn{
			"s1": {
				{Role: "assistant", Tools: []ToolCall{
					{ID: "tu1", Name: "Bash", Summary: "ls"},
				}},
			},
		},
	}
	m.applyToolResult("s1", "tu_unknown", "doesn't matter", false)
	if got := m.conversation["s1"][0].Tools[0].ResultPreview; got != "" {
		t.Fatalf("expected unchanged ResultPreview on miss, got %q", got)
	}
}

// TestApplyToolResult_EmptyArgsNoOp — empty id or preview must not
// scan anything (cheap guard).
func TestApplyToolResult_EmptyArgsNoOp(t *testing.T) {
	m := &Model{conversation: map[string][]Turn{}}
	// Both empty — should not panic on the missing session map entry.
	m.applyToolResult("s1", "", "", false)
	m.applyToolResult("s1", "tu1", "", false)
	m.applyToolResult("s1", "", "preview", false)
}

// TestApplyToolResult_ErrorWithEmptyPreview — an error rejection often
// arrives with an empty body. We still apply it so the toolbox can
// render "✗ rejected" instead of silently looking succeeded.
func TestApplyToolResult_ErrorWithEmptyPreview(t *testing.T) {
	m := &Model{
		conversation: map[string][]Turn{
			"s1": {
				{Role: "assistant", Tools: []ToolCall{
					{ID: "tu1", Name: "Edit", Summary: "/tmp/x"},
				}},
			},
		},
	}
	m.applyToolResult("s1", "tu1", "", true)
	tc := m.conversation["s1"][0].Tools[0]
	if !tc.ResultIsError {
		t.Fatalf("expected ResultIsError=true after splicing rejection")
	}
}

// TestTurnSignature — same role+text but different tool-call IDs must
// produce distinct signatures so live dedup doesn't drop a legit
// repeat.
func TestTurnSignature_DistinctOnToolID(t *testing.T) {
	a := turnSignature("assistant", "let me check",
		[]ToolCall{{ID: "tu1", Name: "Bash"}})
	b := turnSignature("assistant", "let me check",
		[]ToolCall{{ID: "tu2", Name: "Bash"}})
	if a == b {
		t.Fatalf("expected distinct signatures for different tool IDs: %q vs %q", a, b)
	}
}
