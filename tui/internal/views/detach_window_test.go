package views

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestOpenMacosTerminal_BuildsOsascriptCommand asserts the macOS
// branch wraps cmdLine in an `osascript -e <script>` invocation that
// references Terminal.app's `do script` AppleScript verb. We swap
// execCommand with a stub so the test never actually opens a real
// Terminal window — important because Go test runs are headless.
func TestOpenMacosTerminal_BuildsOsascriptCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("skipping macos-only test on %s", runtime.GOOS)
	}
	var capturedName string
	var capturedArgs []string
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	execCommand = func(name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = args
		// /usr/bin/true is a 100% portable no-op so .Start() succeeds
		// without spawning anything user-visible. (osascript itself
		// would open Terminal.app, which is exactly what we don't want
		// in a test.)
		return exec.Command("/usr/bin/true")
	}
	if err := openMacosTerminal(`chubby tui --focus "api" --detached`); err != nil {
		t.Fatalf("openMacosTerminal: %v", err)
	}
	if capturedName != "osascript" {
		t.Fatalf("execCommand name = %q, want osascript", capturedName)
	}
	if len(capturedArgs) != 2 || capturedArgs[0] != "-e" {
		t.Fatalf("execCommand args = %v, want [-e <script>]", capturedArgs)
	}
	script := capturedArgs[1]
	if !strings.Contains(script, `tell application "Terminal"`) {
		t.Fatalf("script missing Terminal tell:\n%s", script)
	}
	if !strings.Contains(script, "do script") {
		t.Fatalf("script missing 'do script':\n%s", script)
	}
	if !strings.Contains(script, "chubby tui --focus") {
		t.Fatalf("script missing chubby command:\n%s", script)
	}
}

// TestOpenLinuxTerminal_PicksFirstAvailable verifies the Linux branch
// honours the priority list: when execLookPath claims gnome-terminal
// is on $PATH, that's the binary we exec — even if konsole is also
// "available." We stub both helpers so no real terminal is spawned.
func TestOpenLinuxTerminal_PicksFirstAvailable(t *testing.T) {
	prevLookPath := execLookPath
	prevCommand := execCommand
	t.Cleanup(func() {
		execLookPath = prevLookPath
		execCommand = prevCommand
	})
	execLookPath = func(file string) (string, error) {
		// Pretend everything is installed; the function should still
		// pick gnome-terminal because it's first in the priority list.
		return "/usr/bin/" + file, nil
	}
	var capturedName string
	var capturedArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = args
		return exec.Command("/usr/bin/true")
	}
	if err := openLinuxTerminal(`chubby tui --focus "api" --detached`); err != nil {
		t.Fatalf("openLinuxTerminal: %v", err)
	}
	if capturedName != "gnome-terminal" {
		t.Fatalf("execCommand name = %q, want gnome-terminal", capturedName)
	}
	if len(capturedArgs) < 4 || capturedArgs[len(capturedArgs)-2] != "-c" {
		t.Fatalf("expected sh -c invocation, got %v", capturedArgs)
	}
	if !strings.Contains(capturedArgs[len(capturedArgs)-1], "chubby tui --focus") {
		t.Fatalf("sh -c arg missing chubby command: %v", capturedArgs)
	}
}

// TestOpenLinuxTerminal_NoTerminalAvailable returns a descriptive
// error listing every candidate so users know which packages would
// unblock /detach.
func TestOpenLinuxTerminal_NoTerminalAvailable(t *testing.T) {
	prevLookPath := execLookPath
	t.Cleanup(func() { execLookPath = prevLookPath })
	execLookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}
	err := openLinuxTerminal("anything")
	if err == nil {
		t.Fatal("expected error when no terminals are installed")
	}
	for _, name := range []string{"gnome-terminal", "konsole", "xterm"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q missing candidate %q", err, name)
		}
	}
}

// TestOpenDetachedWindow_SmokeOnHostOS is a no-spawn smoke check: on
// the current platform, OpenDetachedWindow either succeeds (with our
// stubs) or returns the documented unsupported-OS error. It does NOT
// open a real terminal.
func TestOpenDetachedWindow_SmokeOnHostOS(t *testing.T) {
	prevCommand := execCommand
	prevLookPath := execLookPath
	t.Cleanup(func() {
		execCommand = prevCommand
		execLookPath = prevLookPath
	})
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/usr/bin/true")
	}
	execLookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	err := OpenDetachedWindow("api")
	switch runtime.GOOS {
	case "darwin", "linux":
		if err != nil {
			t.Fatalf("OpenDetachedWindow on %s: %v", runtime.GOOS, err)
		}
	default:
		if err == nil {
			t.Fatalf("expected unsupported-OS error on %s", runtime.GOOS)
		}
	}
}
