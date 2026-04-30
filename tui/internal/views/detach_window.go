// Package views — detach_window.go: spawn a new GUI terminal window
// running `chubby tui --focus <name> --detached` so a single chubby
// session can live in its own OS window. Backs the chub-side
// /detach slash command.
package views

import (
	"fmt"
	"os/exec"
	"runtime"
)

// execCommand is the package-level exec.Command indirection so tests
// can swap it out and inspect the constructed argv WITHOUT actually
// spawning a real terminal window. Tests must restore it via t.Cleanup.
var execCommand = exec.Command

// execLookPath mirrors execCommand for the linux fallback so tests can
// pretend specific terminals are/aren't installed without touching
// $PATH. Defaults to the real exec.LookPath.
var execLookPath = exec.LookPath

// OpenDetachedWindow spawns a new GUI terminal window running
// `chubby tui --focus <name> --detached`. macOS uses osascript to
// drive Terminal.app; Linux walks a priority list of common terminals
// and picks the first one on $PATH. Other OSes return an error
// suggesting the user run the command manually.
//
// The spawned process is detached (we call .Start, not .Run) so the
// originating chubby-tui doesn't block waiting for the new window.
func OpenDetachedWindow(name string) error {
	cmdLine := fmt.Sprintf("chubby tui --focus %q --detached", name)
	switch runtime.GOOS {
	case "darwin":
		return openMacosTerminal(cmdLine)
	case "linux":
		return openLinuxTerminal(cmdLine)
	default:
		return fmt.Errorf("detach not supported on %s — run `chubby tui --focus %s --detached` manually", runtime.GOOS, name)
	}
}

// openMacosTerminal asks Terminal.app via osascript to open a fresh
// window running cmdLine. We use `do script` (which spawns a new
// window by default) and then `activate` so the window comes to the
// front. .Start (not .Run) so we don't block.
func openMacosTerminal(cmdLine string) error {
	script := fmt.Sprintf(`
tell application "Terminal"
    do script %q
    activate
end tell
`, cmdLine)
	cmd := execCommand("osascript", "-e", script)
	return cmd.Start()
}

// openLinuxTerminal walks a priority-ordered list of GUI terminals and
// hands the first one we find on $PATH a `sh -c <cmdLine>` wrapper.
// gnome-terminal goes first because it's the GNOME default (Ubuntu,
// Fedora Workstation); konsole/xterm cover KDE and the lowest common
// denominator; alacritty/kitty are popular among power users. If none
// are installed we return an error listing what we tried.
func openLinuxTerminal(cmdLine string) error {
	candidates := [][]string{
		{"gnome-terminal", "--", "sh", "-c", cmdLine},
		{"konsole", "-e", "sh", "-c", cmdLine},
		{"xterm", "-e", "sh", "-c", cmdLine},
		{"alacritty", "-e", "sh", "-c", cmdLine},
		{"kitty", "sh", "-c", cmdLine},
	}
	for _, c := range candidates {
		if _, err := execLookPath(c[0]); err == nil {
			cmd := execCommand(c[0], c[1:]...)
			return cmd.Start()
		}
	}
	return fmt.Errorf("no GUI terminal found (tried gnome-terminal, konsole, xterm, alacritty, kitty)")
}
