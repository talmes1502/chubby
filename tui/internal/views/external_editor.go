// Package views — external_editor.go: smart detection + spawn of an
// out-of-TUI GUI editor for the "open externally" flow. Priority:
//   1. $CHUBBY_EDITOR (user override; no line-jumping)
//   2. JetBrains: pycharm / charm / idea (with --line N)
//   3. VSCode-family: code / cursor (with --goto file:line)
//   4. Sublime: subl
//
// We deliberately skip $EDITOR — it's almost always a terminal editor
// (vim/nano), and launching that on top of the TUI would scramble the
// screen.
package views

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ExternalEditor describes a launchable GUI editor: the binary to spawn
// plus the argv template. "{file}" / "{line}" placeholders in Args are
// substituted at spawn time.
type ExternalEditor struct {
	Cmd  string
	Args []string
}

// detectCandidates is the priority-ordered list of GUI editors we try
// when $CHUBBY_EDITOR isn't set. Exposed (lowercase) so tests can
// snapshot the order.
var detectCandidates = []ExternalEditor{
	{"pycharm", []string{"--line", "{line}", "{file}"}},
	{"charm", []string{"--line", "{line}", "{file}"}},
	{"idea", []string{"--line", "{line}", "{file}"}},
	{"code", []string{"--goto", "{file}:{line}"}},
	{"cursor", []string{"--goto", "{file}:{line}"}},
	{"subl", []string{"{file}:{line}"}},
}

// lookPath is overridable so tests can stub PATH lookup without
// touching the OS.
var lookPath = exec.LookPath

// DetectExternalEditor returns the first usable editor following the
// priority order documented in the package comment. Returns nil when
// nothing is found — the caller surfaces a helpful error.
func DetectExternalEditor() *ExternalEditor {
	if v := strings.TrimSpace(os.Getenv("CHUBBY_EDITOR")); v != "" {
		return &ExternalEditor{Cmd: v, Args: []string{"{file}"}}
	}
	for _, c := range detectCandidates {
		if _, err := lookPath(c.Cmd); err == nil {
			out := c
			return &out
		}
	}
	return nil
}

// OpenFile spawns the editor as a detached subprocess so the TUI
// doesn't block waiting for it. line is 1-indexed (matches every
// editor's --line / --goto convention); pass 0 or negative to mean "no
// specific line".
func (e ExternalEditor) OpenFile(path string, line int) error {
	if line < 1 {
		line = 1
	}
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		a = strings.ReplaceAll(a, "{file}", path)
		a = strings.ReplaceAll(a, "{line}", strconv.Itoa(line))
		args[i] = a
	}
	cmd := exec.Command(e.Cmd, args...)
	// Detach: don't pipe any std streams. The editor inherits no fd
	// from us, so when the TUI redraws it doesn't fight the editor
	// process for the terminal.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}
