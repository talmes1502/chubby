// Command chubby-tui is the Bubble Tea front-end for chubbyd.
//
// It connects to the daemon's Unix socket, subscribes to the event stream,
// and renders a session list with a focused live-output viewport.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/model"
	"github.com/talmes1502/chubby/tui/internal/rpc"
)

// Version is the chubby-tui binary version. Set via -ldflags by
// GoReleaser at release time so a tagged build identifies itself
// without a separate constant to keep in sync. Local `go build`
// keeps the dev-default below.
var Version = "0.0.0+dev"

// chubbyEnv reads CHUBBY_<name> with CHUB_<name> as a backward-compat
// fallback (mirrors paths.chubby_env() in the Python daemon).
func chubbyEnv(name string) string {
	if v := os.Getenv("CHUBBY_" + name); v != "" {
		return v
	}
	return os.Getenv("CHUB_" + name)
}

func sockPath() string {
	if v := chubbyEnv("SOCK"); v != "" {
		return v
	}
	if v := chubbyEnv("HOME"); v != "" {
		return filepath.Join(v, "hub.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "chubby", "hub.sock")
}

func main() {
	c, err := rpc.Dial(sockPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "chubby-tui: cannot connect to chubbyd at %s: %v\n", sockPath(), err)
		os.Exit(2)
	}
	if _, err := c.Call(context.Background(), "subscribe_events", nil); err != nil {
		fmt.Fprintf(os.Stderr, "chubby-tui: subscribe failed: %v\n", err)
		os.Exit(2)
	}
	// Note: no tea.WithMouseCellMotion() — mouse capture would block the
	// terminal's native text-selection (and copy/paste). We don't have any
	// mouse handlers, so dropping the option restores normal selection.
	model.BinaryVersion = Version
	p := tea.NewProgram(model.New(c), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// On Ctrl+C the Model stashes a "to resume: claude --resume <id>"
	// line in ExitHint so the user can drop straight into claude
	// without re-opening chubby. Printed AFTER the alt-screen tears
	// down so it survives in the parent shell's scrollback.
	if m, ok := final.(model.Model); ok {
		if hint := m.ExitHint(); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}
	}
}
