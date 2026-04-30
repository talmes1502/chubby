package views

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDetectExternalEditor_ChubbyEnvOverride — when $CHUBBY_EDITOR is
// set, it wins over any installed candidate.
func TestDetectExternalEditor_ChubbyEnvOverride(t *testing.T) {
	t.Setenv("CHUBBY_EDITOR", "my-special-editor")
	// Even with all candidates present, env should win.
	defer stubLookPath(func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})()

	ed := DetectExternalEditor()
	if ed == nil {
		t.Fatalf("expected non-nil ExternalEditor")
	}
	if ed.Cmd != "my-special-editor" {
		t.Fatalf("expected Cmd=my-special-editor, got %q", ed.Cmd)
	}
	if len(ed.Args) != 1 || ed.Args[0] != "{file}" {
		t.Fatalf("expected env-override args=[{file}], got %v", ed.Args)
	}
}

// TestDetectExternalEditor_NoneFound — no env var, no editors on PATH:
// returns nil so the caller can surface a helpful error.
func TestDetectExternalEditor_NoneFound(t *testing.T) {
	t.Setenv("CHUBBY_EDITOR", "")
	defer stubLookPath(func(name string) (string, error) {
		return "", errors.New("not found")
	})()

	ed := DetectExternalEditor()
	if ed != nil {
		t.Fatalf("expected nil when no editors found, got %+v", ed)
	}
}

// TestDetectExternalEditor_PycharmFound — fake-PATH that only resolves
// "pycharm" should pick the JetBrains entry.
func TestDetectExternalEditor_PycharmFound(t *testing.T) {
	t.Setenv("CHUBBY_EDITOR", "")
	defer stubLookPath(func(name string) (string, error) {
		if name == "pycharm" {
			return "/usr/local/bin/pycharm", nil
		}
		return "", errors.New("not found")
	})()

	ed := DetectExternalEditor()
	if ed == nil {
		t.Fatalf("expected non-nil ExternalEditor")
	}
	if ed.Cmd != "pycharm" {
		t.Fatalf("expected pycharm, got %q", ed.Cmd)
	}
	if len(ed.Args) < 3 || ed.Args[0] != "--line" {
		t.Fatalf("expected JetBrains --line template, got %v", ed.Args)
	}
}

// TestDetectExternalEditor_VSCodeFallback — pycharm/charm/idea absent
// but code present: VSCode wins.
func TestDetectExternalEditor_VSCodeFallback(t *testing.T) {
	t.Setenv("CHUBBY_EDITOR", "")
	defer stubLookPath(func(name string) (string, error) {
		if name == "code" {
			return "/usr/bin/code", nil
		}
		return "", errors.New("not found")
	})()

	ed := DetectExternalEditor()
	if ed == nil {
		t.Fatalf("expected non-nil ExternalEditor")
	}
	if ed.Cmd != "code" {
		t.Fatalf("expected code, got %q", ed.Cmd)
	}
	// VSCode template uses --goto file:line.
	if len(ed.Args) < 2 || ed.Args[0] != "--goto" {
		t.Fatalf("expected --goto template, got %v", ed.Args)
	}
}

// TestDetectExternalEditor_FakePycharmOnPath — uses a real fake
// pycharm script in a temporary dir prepended to PATH and verifies
// detection without stubbing lookPath. Smoke test for the end-to-end
// PATH lookup (in case stubbing hides a real bug).
func TestDetectExternalEditor_FakePycharmOnPath(t *testing.T) {
	t.Setenv("CHUBBY_EDITOR", "")
	dir := t.TempDir()
	fake := filepath.Join(dir, "pycharm")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	t.Setenv("PATH", dir)
	// Don't stub lookPath here; let the real one run.
	ed := DetectExternalEditor()
	if ed == nil {
		t.Fatalf("expected pycharm to be detected via PATH")
	}
	if ed.Cmd != "pycharm" {
		t.Fatalf("expected pycharm, got %q", ed.Cmd)
	}
}

// TestExternalEditor_OpenFile_BuildsArgs — verifies the {file}/{line}
// templating against a fake pycharm binary that records its argv.
// Side benefit: confirms cmd.Start() actually launches the process.
func TestExternalEditor_OpenFile_BuildsArgs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + "\n"
	bin := filepath.Join(dir, "fakeed")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fakeed: %v", err)
	}
	ed := ExternalEditor{Cmd: bin, Args: []string{"--line", "{line}", "{file}"}}
	if err := ed.OpenFile("/tmp/foo.py", 42); err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	// The spawned process is detached (Start, no Wait) — poll for the
	// argv file to land. 3s budget is generous; the script is trivial.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(out); err == nil && len(data) > 0 {
			expected := "--line\n42\n/tmp/foo.py\n"
			if string(data) != expected {
				t.Fatalf("argv = %q, want %q", string(data), expected)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("fakeed never wrote argv to %s", out)
}

// stubLookPath replaces the package-level lookPath shim with the given
// fn for the duration of a test. Returns a restore-cleanup callable.
func stubLookPath(fn func(string) (string, error)) func() {
	prev := lookPath
	lookPath = fn
	return func() { lookPath = prev }
}
