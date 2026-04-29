// Command chub-tui is the Bubble Tea front-end for chubd.
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

	"github.com/USER/chub/tui/internal/model"
	"github.com/USER/chub/tui/internal/rpc"
)

// Version is the chub-tui binary version.
const Version = "0.1.0"

func sockPath() string {
	if v := os.Getenv("CHUB_SOCK"); v != "" {
		return v
	}
	if v := os.Getenv("CHUB_HOME"); v != "" {
		return filepath.Join(v, "hub.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "hub", "hub.sock")
}

func main() {
	c, err := rpc.Dial(sockPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "chub-tui: cannot connect to chubd at %s: %v\n", sockPath(), err)
		os.Exit(2)
	}
	if _, err := c.Call(context.Background(), "subscribe_events", nil); err != nil {
		fmt.Fprintf(os.Stderr, "chub-tui: subscribe failed: %v\n", err)
		os.Exit(2)
	}
	p := tea.NewProgram(model.New(c), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
