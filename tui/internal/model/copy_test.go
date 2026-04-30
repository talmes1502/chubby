package model

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestFormatConversation_BasicShape verifies the pure formatter renders
// turns separated by blank lines, with user prompts prefixed by the
// "▸ " marker. The clipboard write is a separate concern (tested
// indirectly via the cmd dispatch test below).
func TestFormatConversation_BasicShape(t *testing.T) {
	turns := []Turn{
		{Role: "user", Text: "hello"},
		{Role: "assistant", Text: "hi there"},
		{Role: "user", Text: "ok bye"},
	}
	got := formatConversation(turns)
	want := "▸ hello\n\nhi there\n\n▸ ok bye"
	if got != want {
		t.Fatalf("formatConversation mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatConversation_Empty(t *testing.T) {
	if got := formatConversation(nil); got != "" {
		t.Fatalf("empty turns should yield empty string, got %q", got)
	}
}

// TestCopyConversation_NoFocusedSession returns a nil cmd — there's
// nothing to copy when no session is focused.
func TestCopyConversation_NoFocusedSession(t *testing.T) {
	m := Model{conversation: map[string][]Turn{}}
	if cmd := m.copyConversation(); cmd != nil {
		t.Fatalf("expected nil cmd when no focused session, got non-nil")
	}
}

// TestCopyConversation_NoTurns returns nil cmd when the focused
// session has an empty conversation.
func TestCopyConversation_NoTurns(t *testing.T) {
	m := Model{
		sessions:     []Session{{ID: "s1", Name: "alpha"}},
		focused:      0,
		conversation: map[string][]Turn{"s1": {}},
	}
	if cmd := m.copyConversation(); cmd != nil {
		t.Fatalf("expected nil cmd when conversation empty, got non-nil")
	}
}

// TestCopyConversation_DispatchesCopiedMsg drives copyConversation with
// a populated session and asserts the returned cmd produces a
// copiedMsg with the correct count. We do NOT assert clipboard
// contents — clipboard.WriteAll touches the host clipboard which is
// flaky under headless CI. The pure formatter is covered above.
func TestCopyConversation_DispatchesCopiedMsg(t *testing.T) {
	m := Model{
		sessions: []Session{{ID: "s1", Name: "alpha"}},
		focused:  0,
		conversation: map[string][]Turn{
			"s1": {
				{Role: "user", Text: "ping"},
				{Role: "assistant", Text: "pong"},
			},
		},
	}
	cmd := m.copyConversation()
	if cmd == nil {
		t.Fatalf("expected non-nil cmd, got nil")
	}
	msg := cmd()
	switch v := msg.(type) {
	case copiedMsg:
		if v.count != 2 {
			t.Fatalf("expected count=2, got %d", v.count)
		}
	case errMsg:
		// Headless env without a clipboard backend is acceptable —
		// the cmd still ran and returned a meaningful error.
		if v.err == nil {
			t.Fatalf("errMsg with nil err")
		}
	default:
		t.Fatalf("unexpected msg type %T: %+v", msg, msg)
	}
}

// TestUpdate_CopiedMsg_PushesToast verifies the reducer reacts to
// copiedMsg by appending a toast that says "copied N messages".
func TestUpdate_CopiedMsg_PushesToast(t *testing.T) {
	m := Model{conversation: map[string][]Turn{}}
	out, cmd := m.Update(copiedMsg{count: 3})
	_ = cmd
	got := out.(Model)
	if len(got.toasts) != 1 {
		t.Fatalf("expected 1 toast, got %d", len(got.toasts))
	}
	if !strings.Contains(got.toasts[0].sessionName, "copied 3 messages") {
		t.Fatalf("unexpected toast text: %q", got.toasts[0].sessionName)
	}
}

// Compile-time guard: copyConversation returns a tea.Cmd.
var _ = func() tea.Cmd { return Model{}.copyConversation() }
