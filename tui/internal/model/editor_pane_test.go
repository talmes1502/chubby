package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chubby/tui/internal/rpc"
	"github.com/USER/chubby/tui/internal/views"
)

// makeMainEditorModel returns a Model in ModeMain with a single
// focused session whose Cwd is set, so editor-prompt pre-fill paths
// resolve sensibly.
func makeMainEditorModel(t *testing.T, cwd string) Model {
	t.Helper()
	m := Model{
		mode:           ModeMain,
		compose:        views.NewCompose(),
		conversation:   map[string][]Turn{},
		historyLoaded:  map[string]bool{},
		groupCollapsed: map[string]bool{},
		sessions: []Session{
			{ID: "s1", Name: "alpha", Color: "#abcdef", Status: "idle", Cwd: cwd},
		},
		focused: 0,
	}
	return m
}

// TestCtrlO_EntersEditorPathPrompt — Ctrl+O from ModeMain switches to
// ModeEditor with inPathPrompt=true and visible=true. The textinput is
// pre-filled with the focused session's cwd + "/".
func TestCtrlO_EntersEditorPathPrompt(t *testing.T) {
	m := makeMainEditorModel(t, "/tmp/proj")
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	mm := out.(Model)
	if mm.mode != ModeEditor {
		t.Fatalf("expected ModeEditor, got %v", mm.mode)
	}
	if !mm.editor.inPathPrompt {
		t.Fatalf("expected inPathPrompt=true")
	}
	if !mm.editor.visible {
		t.Fatalf("expected visible=true")
	}
	if got := mm.editor.pathInput.Value(); got != "/tmp/proj/" {
		t.Fatalf("expected prompt prefilled with cwd+/, got %q", got)
	}
}

// TestEditorPathSubmit_LoadsFile — typing a path into the prompt and
// pressing Enter fires loadEditorFile, which reads the file and emits
// editorFileLoadedMsg. We feed that back into Update and assert the
// editor moves out of inPathPrompt with the file content captured.
func TestEditorPathSubmit_LoadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.py")
	if err := os.WriteFile(path, []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := makeMainEditorModel(t, dir)
	// Open the prompt.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = out.(Model)
	// Replace the input with our absolute test path.
	m.editor.pathInput.SetValue(path)
	// Submit.
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if cmd == nil {
		t.Fatalf("expected non-nil cmd from Enter on prompt")
	}
	msg := cmd()
	loaded, ok := msg.(editorFileLoadedMsg)
	if !ok {
		t.Fatalf("expected editorFileLoadedMsg, got %T (%v)", msg, msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected load err: %v", loaded.err)
	}
	if !strings.Contains(loaded.content, "print('hi')") {
		t.Fatalf("expected content to include source, got %q", loaded.content)
	}
	// Feed loaded msg back into Update.
	out, _ = m.Update(loaded)
	m = out.(Model)
	if m.editor.inPathPrompt {
		t.Fatalf("expected inPathPrompt=false after load")
	}
	if m.editor.path != path {
		t.Fatalf("expected path=%q, got %q", path, m.editor.path)
	}
	if !m.editor.visible {
		t.Fatalf("expected visible=true after load")
	}
	// Path should land in recentPaths.
	if len(m.editor.recentPaths) == 0 || m.editor.recentPaths[0] != path {
		t.Fatalf("expected recentPaths head=%q, got %v", path, m.editor.recentPaths)
	}
}

// TestCtrlE_TogglesVisibility — once a file has been loaded, Ctrl+E
// toggles the editor's visibility flag without changing the loaded
// path.
func TestCtrlE_TogglesVisibility(t *testing.T) {
	m := makeMainEditorModel(t, "/tmp")
	m.editor.path = "/tmp/foo.py"
	m.editor.highlighted = "x = 1\n"
	m.editor.visible = true
	// Sanity: Ctrl+E toggles off.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = out.(Model)
	if m.editor.visible {
		t.Fatalf("expected visible=false after first Ctrl+E")
	}
	// Toggle back on.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = out.(Model)
	if !m.editor.visible {
		t.Fatalf("expected visible=true after second Ctrl+E")
	}
	// The path should be intact across toggles.
	if m.editor.path != "/tmp/foo.py" {
		t.Fatalf("expected path preserved, got %q", m.editor.path)
	}
}

// TestCtrlE_NoFileLoaded_OpensPrompt — Ctrl+E with no file loaded
// routes to the path prompt instead of being a confusing no-op.
func TestCtrlE_NoFileLoaded_OpensPrompt(t *testing.T) {
	m := makeMainEditorModel(t, "/tmp")
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	mm := out.(Model)
	if mm.mode != ModeEditor || !mm.editor.inPathPrompt {
		t.Fatalf("expected Ctrl+E with no file to open path prompt, mode=%v inPrompt=%v",
			mm.mode, mm.editor.inPathPrompt)
	}
}

// TestEditor_EscClosesPane — Esc while viewing closes both the visible
// flag and returns to ModeMain.
func TestEditor_EscClosesPane(t *testing.T) {
	m := makeMainEditorModel(t, "/tmp")
	m.editor.path = "/tmp/foo.py"
	m.editor.highlighted = "x"
	m.editor.visible = true
	m.mode = ModeEditor
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := out.(Model)
	if mm.mode != ModeMain {
		t.Fatalf("expected ModeMain after Esc, got %v", mm.mode)
	}
	if mm.editor.visible {
		t.Fatalf("expected visible=false after Esc")
	}
}

// TestEditor_PathDetectionFromTranscript — driving a transcript_message
// containing a /tmp/foo.py mention populates m.editor.recentPaths with
// the path at the head.
func TestEditor_PathDetectionFromTranscript(t *testing.T) {
	m := makeMainEditorModel(t, "/tmp")
	ev := evMsg(rpc.Event{
		Method: "event",
		Params: map[string]any{
			"event_method": "transcript_message",
			"event_params": map[string]any{
				"session_id": "s1",
				"role":       "assistant",
				"text":       "Look at /tmp/foo.py:42 and also /tmp/bar.go",
				"ts":         float64(1),
			},
		},
	})
	out, _ := m.Update(ev)
	mm := out.(Model)
	// Both paths should be recorded; bar.go appears later in the
	// string so it lands at the head after pushRecentPath dedupe (we
	// push in textual order; later matches replace earlier as head).
	if len(mm.editor.recentPaths) < 2 {
		t.Fatalf("expected >=2 recentPaths, got %d (%v)",
			len(mm.editor.recentPaths), mm.editor.recentPaths)
	}
	// We push in match order, so the most-recent at head is /tmp/bar.go.
	if mm.editor.recentPaths[0] != "/tmp/bar.go" {
		t.Fatalf("expected /tmp/bar.go at head, got %q", mm.editor.recentPaths[0])
	}
	// Note: pathRE strips the trailing :42 from /tmp/foo.py.
	found := false
	for _, p := range mm.editor.recentPaths {
		if p == "/tmp/foo.py" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected /tmp/foo.py among recent paths, got %v", mm.editor.recentPaths)
	}
}

// TestCtrlBracket_OpensMostRecent — Ctrl+] with a populated
// recentPaths slice fires a load for the head entry.
func TestCtrlBracket_OpensMostRecent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.py")
	if err := os.WriteFile(path, []byte("y = 2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := makeMainEditorModel(t, dir)
	m.editor.recentPaths = []string{path}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]"), Alt: false})
	// tea reports Ctrl+] as Type=KeyCtrlCloseBracket on most terminals.
	// If the runes path wasn't right, emit it via the canonical key.
	_ = out
	_ = cmd
	// Try the canonical chord.
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlCloseBracket})
	m = out.(Model)
	if cmd == nil {
		t.Fatalf("expected non-nil cmd from Ctrl+]")
	}
	msg := cmd()
	loaded, ok := msg.(editorFileLoadedMsg)
	if !ok {
		t.Fatalf("expected editorFileLoadedMsg, got %T", msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected err: %v", loaded.err)
	}
	if loaded.path != path {
		t.Fatalf("expected loaded path=%q, got %q", path, loaded.path)
	}
}

// TestCtrlBracket_NoRecent_OpensPrompt — Ctrl+] with empty recentPaths
// falls back to the path prompt so the user understands what to do.
func TestCtrlBracket_NoRecent_OpensPrompt(t *testing.T) {
	m := makeMainEditorModel(t, "/tmp")
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlCloseBracket})
	mm := out.(Model)
	if mm.mode != ModeEditor || !mm.editor.inPathPrompt {
		t.Fatalf("expected path prompt fallback, mode=%v inPrompt=%v",
			mm.mode, mm.editor.inPathPrompt)
	}
}

// TestEditor_Scroll — Down/Up/g/G mutate scrollOffset as expected.
func TestEditor_Scroll(t *testing.T) {
	m := makeMainEditorModel(t, "/tmp")
	m.editor.path = "/tmp/foo.py"
	m.editor.highlighted = "a\nb\nc\nd\ne\n"
	m.editor.visible = true
	m.mode = ModeEditor

	// Down: 0 -> 1.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	if m.editor.scrollOffset != 1 {
		t.Fatalf("expected scrollOffset=1, got %d", m.editor.scrollOffset)
	}
	// G: jump to bottom — 5 \n means 5 lines of newline-count.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = out.(Model)
	if m.editor.scrollOffset != 5 {
		t.Fatalf("expected scrollOffset=5 after G, got %d", m.editor.scrollOffset)
	}
	// g: jump to top.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = out.(Model)
	if m.editor.scrollOffset != 0 {
		t.Fatalf("expected scrollOffset=0 after g, got %d", m.editor.scrollOffset)
	}
}

// TestSplitPathLine — verifies the regex-based splitter for paths
// with line suffixes.
func TestSplitPathLine(t *testing.T) {
	cases := []struct {
		in       string
		wantPath string
		wantLine int
	}{
		{"/tmp/foo.py", "/tmp/foo.py", 0},
		{"/tmp/foo.py:42", "/tmp/foo.py", 42},
		{"/some/deeply/nested/path/with-dash.go:123", "/some/deeply/nested/path/with-dash.go", 123},
	}
	for _, c := range cases {
		gotPath, gotLine := splitPathLine(c.in)
		if gotPath != c.wantPath || gotLine != c.wantLine {
			t.Errorf("splitPathLine(%q) = (%q, %d), want (%q, %d)",
				c.in, gotPath, gotLine, c.wantPath, c.wantLine)
		}
	}
}

// TestPushRecentPath_Dedupes — adding a path that already exists moves
// it to head without duplicating.
func TestPushRecentPath_Dedupes(t *testing.T) {
	s := []string{"/a", "/b", "/c"}
	out := pushRecentPath(s, "/b")
	if len(out) != 3 {
		t.Fatalf("expected len 3 (deduped), got %d", len(out))
	}
	if out[0] != "/b" {
		t.Fatalf("expected /b at head, got %v", out)
	}
}

// TestPushRecentPath_Caps — slice never exceeds editorRecentPathsCap.
func TestPushRecentPath_Caps(t *testing.T) {
	var s []string
	for i := 0; i < editorRecentPathsCap+5; i++ {
		s = pushRecentPath(s, "/p"+itoa(i))
	}
	if len(s) > editorRecentPathsCap {
		t.Fatalf("slice exceeded cap: %d > %d", len(s), editorRecentPathsCap)
	}
}

// itoa is a tiny strconv-free integer formatter used in this test
// file. Keeps the imports minimal.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	if n < 0 {
		b = append(b, '-')
		n = -n
	}
	var d []byte
	for n > 0 {
		d = append(d, byte('0'+n%10))
		n /= 10
	}
	for i := len(d) - 1; i >= 0; i-- {
		b = append(b, d[i])
	}
	return string(b)
}
