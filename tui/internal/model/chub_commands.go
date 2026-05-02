// Package model — chub_commands.go: chubby-side slash command
// handlers (/color, /rename, /tag, /refresh-claude, /movetofolder,
// /removefromfolder, /detach). These are intercepted by the TUI rather
// than forwarded to claude — the compose-bar dispatcher in sendComposed
// calls splitChubCommand to recognise the head, then routes to the
// right doChub* helper.
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/USER/chubby/tui/internal/views"
)

// ChubCommand identifies a chubby-side slash command. Typed-string
// (rather than iota) so the value is the same string the user types
// — useful when storing palette history or logging, and lets us
// pass it through string formatting unchanged.
type ChubCommand string

const (
	ChubCmdColor            ChubCommand = "/color"
	ChubCmdRename           ChubCommand = "/rename"
	ChubCmdTag              ChubCommand = "/tag"
	ChubCmdRefreshClaude    ChubCommand = "/refresh-claude"
	ChubCmdMoveToFolder     ChubCommand = "/movetofolder"
	ChubCmdRemoveFromFolder ChubCommand = "/removefromfolder"
	ChubCmdDetach           ChubCommand = "/detach"
)

// chubCommandHeads is the dispatch table for splitChubCommand:
// ordered longest-first so "/removefromfolder" wins over a future
// "/remove*" and "/movetofolder" wins over a future "/move*".
var chubCommandHeads = []ChubCommand{
	ChubCmdRemoveFromFolder,
	ChubCmdMoveToFolder,
	ChubCmdRefreshClaude,
	ChubCmdDetach,
	ChubCmdColor,
	ChubCmdRename,
	ChubCmdTag,
}

// splitChubCommand recognises a chubby-side slash command head at the
// start of the trimmed compose text and returns (cmd, remainder-
// trimmed, true). The remainder may be empty — the caller decides
// how to surface a usage error. Returns ("", "", false) for anything
// else, leaving the regular inject path to handle it.
func splitChubCommand(s string) (cmd ChubCommand, arg string, ok bool) {
	for _, head := range chubCommandHeads {
		hs := string(head)
		if s == hs {
			return head, "", true
		}
		if strings.HasPrefix(s, hs+" ") {
			return head, strings.TrimSpace(s[len(hs)+1:]), true
		}
	}
	return "", "", false
}

// dispatchChubCommand is the single entry point for routing a parsed
// ChubCommand to its tea.Cmd handler. Both the rail palette and the
// legacy compose-bar's sendComposed call this — one switch, one
// truth. Returns nil for unknown commands (caller's responsibility
// to surface a usage error if appropriate).
func (m Model) dispatchChubCommand(cmd ChubCommand, arg string) tea.Cmd {
	switch cmd {
	case ChubCmdColor:
		return m.doChubColor(arg)
	case ChubCmdRename:
		return m.doChubRename(arg)
	case ChubCmdTag:
		return m.doChubTag(arg)
	case ChubCmdRefreshClaude:
		return m.doChubRefreshClaude()
	case ChubCmdMoveToFolder:
		return m.doChubMoveToFolder(arg)
	case ChubCmdRemoveFromFolder:
		return m.doChubRemoveFromFolder()
	case ChubCmdDetach:
		return m.doChubDetach()
	}
	return nil
}

// chubColorRE matches a strict #RRGGBB hex literal.
var chubColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// chubPalette mirrors the daemon-side PALETTE in src/chubby/daemon/colors.py.
// Used by /color so users can say "/color 0" or "/color red" without
// memorising hex codes. Order matches the daemon palette so palette
// indexes line up.
var chubPalette = []string{
	"#5fafff", // 0  bright blue
	"#ff8787", // 1  salmon
	"#87d787", // 2  mint
	"#ffaf5f", // 3  orange
	"#d787d7", // 4  magenta
	"#5fd7d7", // 5  cyan
	"#d7d787", // 6  olive
	"#af87ff", // 7  lavender
	"#ff5faf", // 8  pink
	"#87afff", // 9  periwinkle
	"#d7af87", // 10 tan
	"#87d7af", // 11 seafoam
	"#ff5f5f", // 12 coral (~ red)
	"#5fd75f", // 13 lime  (~ green)
	"#d7d7d7", // 14 light grey
	"#ffffaf", // 15 cream (~ yellow)
}

// chubColorNames maps friendly names to palette colours.
var chubColorNames = map[string]string{
	"blue":       "#5fafff",
	"salmon":     "#ff8787",
	"mint":       "#87d787",
	"orange":     "#ffaf5f",
	"magenta":    "#d787d7",
	"purple":     "#d787d7",
	"cyan":       "#5fd7d7",
	"olive":      "#d7d787",
	"lavender":   "#af87ff",
	"pink":       "#ff5faf",
	"periwinkle": "#87afff",
	"tan":        "#d7af87",
	"seafoam":    "#87d7af",
	"red":        "#ff5f5f",
	"coral":      "#ff5f5f",
	"green":      "#5fd75f",
	"lime":       "#5fd75f",
	"grey":       "#d7d7d7",
	"gray":       "#d7d7d7",
	"white":      "#d7d7d7",
	"cream":      "#ffffaf",
	"yellow":     "#ffffaf",
}

// resolveChubColor turns a /color argument into a #RRGGBB hex.
// Accepts: "" (error), "#RRGGBB", "0".."15" (palette index), or a
// case-insensitive friendly name.
func resolveChubColor(arg string) (string, error) {
	a := strings.TrimSpace(arg)
	if a == "" {
		return "", fmt.Errorf("usage: /color <#RRGGBB | 0-15 | name (e.g. green, blue, pink)>")
	}
	if chubColorRE.MatchString(a) {
		return a, nil
	}
	// Palette index?
	if idx, err := strconv.Atoi(a); err == nil {
		if idx < 0 || idx >= len(chubPalette) {
			return "", fmt.Errorf("palette index out of range (0-%d): %d", len(chubPalette)-1, idx)
		}
		return chubPalette[idx], nil
	}
	// Friendly name?
	if hex, ok := chubColorNames[strings.ToLower(a)]; ok {
		return hex, nil
	}
	return "", fmt.Errorf("color %q not recognised — use #RRGGBB, an index 0-%d, or a name like green/blue/pink",
		a, len(chubPalette)-1)
}

// doChubColor fires the recolor_session RPC for the focused session.
// Accepts hex, palette index, or a friendly colour name (see
// resolveChubColor).
func (m Model) doChubColor(color string) tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	sid := s.ID
	c := m.client
	return func() tea.Msg {
		hex, err := resolveChubColor(color)
		if err != nil {
			return composeFailedMsg{err}
		}
		if _, err := c.Call(context.Background(), "recolor_session",
			map[string]any{"id": sid, "color": hex}); err != nil {
			return composeFailedMsg{err}
		}
		return chubCommandDoneMsg{}
	}
}

// doChubRename fires the rename_session RPC for the focused session.
// Empty names are rejected up front (the daemon would reject them too,
// but failing here means we don't burn a round-trip on a typo).
func (m Model) doChubRename(name string) tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	sid := s.ID
	c := m.client
	return func() tea.Msg {
		if name == "" {
			return composeFailedMsg{fmt.Errorf("name required")}
		}
		if _, err := c.Call(context.Background(), "rename_session",
			map[string]any{"id": sid, "name": name}); err != nil {
			return composeFailedMsg{err}
		}
		return chubCommandDoneMsg{}
	}
}

// doChubTag parses a "+foo -bar" spec and fires set_session_tags. Lone
// "+" / "-" tokens (no name) are silently dropped; an empty add+remove
// pair yields a usage error so the user sees what shape the command
// expects.
func (m Model) doChubTag(spec string) tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	sid := s.ID
	c := m.client
	return func() tea.Msg {
		var add, remove []string
		for _, tok := range strings.Fields(spec) {
			if strings.HasPrefix(tok, "+") && len(tok) > 1 {
				add = append(add, tok[1:])
			} else if strings.HasPrefix(tok, "-") && len(tok) > 1 {
				remove = append(remove, tok[1:])
			}
		}
		if len(add) == 0 && len(remove) == 0 {
			return composeFailedMsg{fmt.Errorf("usage: /tag +foo -bar")}
		}
		if _, err := c.Call(context.Background(), "set_session_tags",
			map[string]any{"id": sid, "add": add, "remove": remove}); err != nil {
			return composeFailedMsg{err}
		}
		return chubCommandDoneMsg{}
	}
}

// doChubRefreshClaude fires the refresh_claude_session RPC for the
// focused session. The daemon pushes a restart_claude event over the
// wrapper's writer; the wrapper SIGTERMs claude and re-launches with
// ``claude --resume <sid>``. The chubby session row stays put; the JSONL
// stays put; only settings/MCP/hooks reload.
//
// We surface a short toast ("refreshing <name>…") so the user knows
// something happened — the actual restart is a few seconds of latency
// during which the viewport will look frozen.
func (m Model) doChubRefreshClaude() tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	sid := s.ID
	name := s.Name
	c := m.client
	return func() tea.Msg {
		if _, err := c.Call(context.Background(), "refresh_claude_session",
			map[string]any{"id": sid}); err != nil {
			return composeFailedMsg{err}
		}
		return chubCommandDoneMsg{toast: fmt.Sprintf("refreshing %s…", name)}
	}
}

// doChubMoveToFolder assigns the focused session to a folder. The
// folder is created implicitly if it doesn't exist yet — matches the
// "any non-empty arg works" UX of the other chub-side slash commands.
// Empty arg is a usage error.
//
// Folders state lives entirely on the TUI side; no daemon RPC is fired.
// We still emit chubCommandDoneMsg so the standard "clear compose +
// refresh" reducer path runs and m.folders gets re-loaded from disk.
func (m Model) doChubMoveToFolder(folder string) tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	sid := s.ID
	folder = strings.TrimSpace(folder)
	return func() tea.Msg {
		if folder == "" {
			return composeFailedMsg{fmt.Errorf("usage: /movetofolder <name>")}
		}
		st := LoadFolders()
		st.Assign(folder, sid)
		if err := SaveFolders(st); err != nil {
			return composeFailedMsg{err}
		}
		return chubCommandDoneMsg{}
	}
}

// doChubRemoveFromFolder removes the focused session from any folder
// it's in. No-op (still chubCommandDoneMsg) when the session isn't in
// a folder so the user gets the same "compose cleared" feedback either
// way.
func (m Model) doChubRemoveFromFolder() tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	sid := s.ID
	return func() tea.Msg {
		st := LoadFolders()
		st.Unassign(sid)
		if err := SaveFolders(st); err != nil {
			return composeFailedMsg{err}
		}
		return chubCommandDoneMsg{}
	}
}

// openExternalClaudeFn is the package-level indirection for the
// views-side terminal-spawn helper so detach_test.go can swap it for
// a stub without actually launching a real Terminal window.
var openExternalClaudeFn = views.OpenExternalClaude

// doChubDetach releases the focused session from chubby's management
// and re-opens a real ``claude --resume <id>`` in a new GUI terminal
// window. The chubby-managed wrapper (and its claude child) are
// killed; the in-memory registry entry is removed, so the session
// disappears from chubby's rail. The on-disk JSONL is unchanged, so
// the new external claude continues the same conversation.
//
// On success we surface a chubCommandDoneMsg with a toast so the user
// gets the standard "compose cleared" feedback. RPC failure or a
// missing claude_session_id come back as composeFailedMsg.
//
// Spawn-window failure is non-fatal: the daemon-side release already
// succeeded (the session is GONE from chubby), so we still return
// chubCommandDoneMsg — just with a toast that flags the spawn error.
// Falling back to composeFailedMsg there would mislead the user into
// thinking the release didn't happen.
func (m Model) doChubDetach() tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return func() tea.Msg {
			return composeFailedMsg{fmt.Errorf("no session focused")}
		}
	}
	sid := s.ID
	name := s.Name
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "release_session",
			map[string]any{"id": sid})
		if err != nil {
			return composeFailedMsg{fmt.Errorf("detach failed: %w", err)}
		}
		var r struct {
			ClaudeSessionID string `json:"claude_session_id"`
			Cwd             string `json:"cwd"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return composeFailedMsg{fmt.Errorf("detach: %w", err)}
		}
		if r.ClaudeSessionID == "" {
			// Daemon should have rejected this with INVALID_PAYLOAD,
			// but defend against an unexpected empty result.
			return composeFailedMsg{fmt.Errorf(
				"detach: daemon returned no claude_session_id",
			)}
		}
		// Open a real claude in a new GUI terminal — claude --resume
		// <id> from the session's cwd. This is NOT another chubby tui;
		// the user continues talking to the same conversation in a
		// normal terminal, with no chubby wrapper.
		if err := openExternalClaudeFn(r.ClaudeSessionID, r.Cwd); err != nil {
			// Don't fail the whole detach — daemon-side release
			// already succeeded, the session is gone. Just toast the
			// spawn-window error so the user knows to open it manually.
			return chubCommandDoneMsg{toast: fmt.Sprintf(
				"released %s; could not open new window: %v", name, err,
			)}
		}
		return chubCommandDoneMsg{toast: fmt.Sprintf(
			"released %s into a new terminal", name,
		)}
	}
}
