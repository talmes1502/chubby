package views

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestOpenMacosTerminalCmd_BuildsOsascriptCommand asserts the macOS
// branch wraps cmdLine in an `osascript -e <script>` invocation that
// references Terminal.app's `do script` AppleScript verb. We swap
// execCommand with a stub so the test never actually opens a real
// Terminal window — important because Go test runs are headless.
func TestOpenMacosTerminalCmd_BuildsOsascriptCommand(t *testing.T) {
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
	if err := openMacosTerminalCmd(`cd "/tmp/proj" && claude --resume "abc"`); err != nil {
		t.Fatalf("openMacosTerminalCmd: %v", err)
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
	if !strings.Contains(script, "claude --resume") {
		t.Fatalf("script missing claude --resume command:\n%s", script)
	}
}

// TestOpenLinuxTerminalCmd_PicksFirstAvailable verifies the Linux
// branch honours the priority list: when execLookPath claims
// gnome-terminal is on $PATH, that's the binary we exec — even if
// konsole is also "available." We stub both helpers so no real
// terminal is spawned.
func TestOpenLinuxTerminalCmd_PicksFirstAvailable(t *testing.T) {
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
	if err := openLinuxTerminalCmd(`cd "/tmp/proj" && claude --resume "abc"`); err != nil {
		t.Fatalf("openLinuxTerminalCmd: %v", err)
	}
	if capturedName != "gnome-terminal" {
		t.Fatalf("execCommand name = %q, want gnome-terminal", capturedName)
	}
	if len(capturedArgs) < 4 || capturedArgs[len(capturedArgs)-2] != "-c" {
		t.Fatalf("expected sh -c invocation, got %v", capturedArgs)
	}
	if !strings.Contains(capturedArgs[len(capturedArgs)-1], "claude --resume") {
		t.Fatalf("sh -c arg missing claude --resume command: %v", capturedArgs)
	}
}

// TestOpenLinuxTerminalCmd_NoTerminalAvailable returns a descriptive
// error listing every candidate so users know which packages would
// unblock /detach.
func TestOpenLinuxTerminalCmd_NoTerminalAvailable(t *testing.T) {
	prevLookPath := execLookPath
	t.Cleanup(func() { execLookPath = prevLookPath })
	execLookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}
	err := openLinuxTerminalCmd("anything")
	if err == nil {
		t.Fatal("expected error when no terminals are installed")
	}
	for _, name := range []string{"gnome-terminal", "konsole", "xterm"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q missing candidate %q", err, name)
		}
	}
}

// TestOpenExternalClaude_SmokeOnHostOS is a no-spawn smoke check: on
// the current platform, OpenExternalClaude either succeeds (with our
// stubs) or returns the documented unsupported-OS error. It does NOT
// open a real terminal.
func TestOpenExternalClaude_SmokeOnHostOS(t *testing.T) {
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
	err := OpenExternalClaude("abcdef01-0000-0000-0000-000000000000", "/tmp/proj")
	switch runtime.GOOS {
	case "darwin", "linux":
		if err != nil {
			t.Fatalf("OpenExternalClaude on %s: %v", runtime.GOOS, err)
		}
	default:
		if err == nil {
			t.Fatalf("expected unsupported-OS error on %s", runtime.GOOS)
		}
	}
}

// TestOpenExternalClaude_BuildsCdAndResumeCommand verifies the
// composed shell command line includes a `cd` to the cwd and
// `claude --resume <id>` — this is the heart of the contract.
func TestOpenExternalClaude_BuildsCdAndResumeCommand(t *testing.T) {
	prevCommand := execCommand
	prevLookPath := execLookPath
	t.Cleanup(func() {
		execCommand = prevCommand
		execLookPath = prevLookPath
	})
	var capturedScript string
	execCommand = func(name string, args ...string) *exec.Cmd {
		// Capture whichever arg holds our shell command line:
		// macos -> args[1] (after -e); linux -> last arg (after sh -c).
		if len(args) > 0 {
			capturedScript = args[len(args)-1]
		}
		return exec.Command("/usr/bin/true")
	}
	execLookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("skipping on %s", runtime.GOOS)
	}
	if err := OpenExternalClaude("the-sid", "/tmp/proj with space"); err != nil {
		t.Fatalf("OpenExternalClaude: %v", err)
	}
	// On macOS the captured arg is the AppleScript; the shell command
	// line is itself %q-escaped inside ``do script "..."``, so the
	// inner quotes appear as \". On Linux it's the raw shell command
	// line. Either way the cwd path and the claude --resume tokens
	// must be present somewhere.
	if !strings.Contains(capturedScript, "/tmp/proj with space") {
		t.Fatalf("script missing cwd path: %s", capturedScript)
	}
	if !strings.Contains(capturedScript, "claude --resume") {
		t.Fatalf("script missing claude --resume: %s", capturedScript)
	}
	if !strings.Contains(capturedScript, "the-sid") {
		t.Fatalf("script missing session id: %s", capturedScript)
	}
	if !strings.Contains(capturedScript, "cd ") {
		t.Fatalf("script missing cd: %s", capturedScript)
	}
}
