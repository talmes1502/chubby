// Package views — detach_window.go: spawn a new GUI terminal window
// running an arbitrary shell command line. Backs the chub-side
// /detach slash command, which now releases a session from chubby's
// management and re-opens a real `claude --resume <id>` outside it.
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

// OpenExternalClaude spawns a new GUI terminal window that runs
// `cd <cwd> && claude --resume <session-id>`. The session is no longer
// chubby-managed; the user gets a normal claude in a normal terminal,
// with the same JSONL transcript so the conversation continues.
//
// macOS uses osascript to drive Terminal.app; Linux walks a priority
// list of common terminals and picks the first one on $PATH. Other
// OSes return an error suggesting the user run the command manually.
//
// The spawned process is detached (we call .Start, not .Run) so the
// originating chubby-tui doesn't block waiting for the new window.
func OpenExternalClaude(claudeSessionID, cwd string) error {
	// Quote both substitutions through %q so cwds with spaces / quotes
	// can't break out of the shell command line.
	cmdLine := fmt.Sprintf("cd %q && claude --resume %q", cwd, claudeSessionID)
	switch runtime.GOOS {
	case "darwin":
		return openMacosTerminalCmd(cmdLine)
	case "linux":
		return openLinuxTerminalCmd(cmdLine)
	default:
		return fmt.Errorf(
			"detach not supported on %s — run `claude --resume %s` from %s manually",
			runtime.GOOS, claudeSessionID, cwd,
		)
	}
}

// openMacosTerminalCmd asks Terminal.app via osascript to open a fresh
// window running cmdLine. We use `do script` (which spawns a new
// window by default) and then `activate` so the window comes to the
// front. .Start (not .Run) so we don't block.
//
// Terminal.app's `do script` defaults to the user's Default profile
// (Preferences → General → "On startup, open: New window with profile")
// — NOT the profile of the currently-active tab. That can flip a
// dark-theme chubby pane into a light-theme detached window, which
// reads as a regression. Inherit the chubby tab's settings so the
// detached claude opens in the same theme.
func openMacosTerminalCmd(cmdLine string) error {
	script := fmt.Sprintf(`
tell application "Terminal"
    activate
    set chubbySettings to missing value
    try
        set chubbySettings to current settings of selected tab of front window
    end try
    set newTab to do script %q
    if chubbySettings is not missing value then
        try
            set current settings of newTab to chubbySettings
        end try
    end if
end tell
`, cmdLine)
	cmd := execCommand("osascript", "-e", script)
	return cmd.Start()
}

// openLinuxTerminalCmd walks a priority-ordered list of GUI terminals
// and hands the first one we find on $PATH a `sh -c <cmdLine>` wrapper.
// gnome-terminal goes first because it's the GNOME default (Ubuntu,
// Fedora Workstation); konsole/xterm cover KDE and the lowest common
// denominator; alacritty/kitty are popular among power users. If none
// are installed we return an error listing what we tried.
func openLinuxTerminalCmd(cmdLine string) error {
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
