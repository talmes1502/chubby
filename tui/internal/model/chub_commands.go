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
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/talmes1502/chubby/tui/internal/views"
)

// ChubCommand identifies a chubby-side slash command. Typed-string
// (rather than iota) so the value matches what the user types in
// the rail palette — useful for logging and so palette history
// could round-trip through display unchanged.
//
// Bare names (no leading "/") because the rail palette doesn't
// require a slash prefix; splitChubCommand strips one if present
// so inline-in-claude usage (which still uses "/" as the chubby-
// vs-claude disambiguator) keeps working.
type ChubCommand string

const (
	ChubCmdColor            ChubCommand = "color"
	ChubCmdRename           ChubCommand = "rename"
	ChubCmdTag              ChubCommand = "tag"
	ChubCmdRefreshClaude    ChubCommand = "refresh-claude"
	ChubCmdMoveToFolder     ChubCommand = "movetofolder"
	ChubCmdRemoveFromFolder ChubCommand = "removefromfolder"
	ChubCmdDetach           ChubCommand = "detach"
	// ChubCmdRestart is a friendlier alias for refresh-claude. Same
	// underlying RPC; users discover it via "?" or palette autocomplete
	// without having to remember the longer hyphenated form.
	ChubCmdRestart ChubCommand = "restart"
	// ChubCmdClone duplicates the focused session (Phase 8a) — same
	// cwd / branch / tags / folder, name auto-suffixed with -2/-3/...
	// to avoid the daemon's UNIQUE constraint.
	ChubCmdClone ChubCommand = "clone"
	// ChubCmdReleaseAll runs full teardown (release_session) on every
	// non-dead, non-readonly session. Requires the literal arg
	// ``confirm`` so a stray ``:release-all`` doesn't wipe the rail.
	ChubCmdReleaseAll ChubCommand = "release-all"
	// ChubCmdRun starts a long-running ``run`` command from the
	// project's .chubby/config.json (e.g. ``bun dev``). The arg is
	// the index into the ``run`` array (0-based). The process lives
	// for the session's lifetime and is killed automatically on
	// release/detach.
	ChubCmdRun ChubCommand = "run"
	// ChubCmdStopRun stops a running ``run`` command for the focused
	// session. Arg is the index.
	ChubCmdStopRun ChubCommand = "stop-run"
)

// chubCommandHeads is the dispatch table for splitChubCommand:
// ordered longest-first so "removefromfolder" wins over a future
// "remove*" and "movetofolder" wins over a future "move*".
var chubCommandHeads = []ChubCommand{
	ChubCmdRemoveFromFolder,
	ChubCmdMoveToFolder,
	ChubCmdRefreshClaude,
	ChubCmdReleaseAll,
	ChubCmdStopRun,
	ChubCmdRestart,
	ChubCmdDetach,
	ChubCmdColor,
	ChubCmdClone,
	ChubCmdRename,
	ChubCmdRun,
	ChubCmdTag,
}

// AllChubCommands returns the dispatch table — used by the rail
// palette's Tab-autocomplete to enumerate matchable heads.
func AllChubCommands() []ChubCommand {
	out := make([]ChubCommand, len(chubCommandHeads))
	copy(out, chubCommandHeads)
	return out
}

// ChubCommandPlaceholder is the placeholder text shown in the rail
// command palette when it's empty. Generated from the ChubCommand
// constants so adding a new command (movetofolder, color, …) shows
// up automatically without a second source to keep in sync.
func ChubCommandPlaceholder() string {
	parts := make([]string, len(chubCommandHeads))
	for i, h := range chubCommandHeads {
		parts[i] = string(h)
	}
	// Sort alphabetically for stable display order — chubCommandHeads
	// is dispatch-ordered (longest-first), which is the wrong sort
	// for human reading.
	sort.Strings(parts)
	return strings.Join(parts, ", ") + "  (Tab to complete)"
}

// splitChubCommand recognises a chubby-side command head at the
// start of the trimmed text and returns (cmd, remainder-trimmed,
// true). Accepts both bare names ("color blue") and slash-prefixed
// ("/color blue") so the rail palette and inline-in-claude paths
// can share parser. Returns ("", "", false) for anything else.
func splitChubCommand(s string) (cmd ChubCommand, arg string, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "/")
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

// chubCommandArgs returns the autocomplete candidates for a given
// command's argument slot. Empty slice = "free-text arg, no
// suggestions" (e.g. /rename takes any name). Used by the rail
// palette's Tab completer.
func (m Model) chubCommandArgs(cmd ChubCommand) []string {
	switch cmd {
	case ChubCmdColor:
		// Friendly color names + palette indexes. Hex codes are
		// open-ended so we don't enumerate them.
		out := make([]string, 0, len(chubColorNames)+len(chubPalette))
		for name := range chubColorNames {
			out = append(out, name)
		}
		sort.Strings(out)
		for i := range chubPalette {
			out = append(out, fmt.Sprintf("%d", i))
		}
		return out
	case ChubCmdMoveToFolder:
		// Existing folder names, alphabetical. Plus a hint that
		// any new name creates the folder — communicated via
		// placeholder, not enumerated here.
		return m.folders.AllFolderNames()
	}
	return nil
}

// chubCommandComplete picks the next completion for a typed prefix,
// cycling through matches on repeated calls (cycleIdx % matches).
// Returns the completed string + whether a completion was applied.
//
// Three states based on what's typed:
//   1) empty / partial command head — complete the head, e.g. "co"
//      → "color", "move" → "movetofolder".
//   2) full command + space + partial arg — complete the arg from
//      the head's argument table.
//   3) full command, no args yet — append a space if the head takes
//      args, otherwise no-op.
func (m Model) chubCommandComplete(input string, cycleIdx int) (out string, ok bool, total int) {
	input = strings.TrimSpace(input)
	hasSlash := strings.HasPrefix(input, "/")
	bare := strings.TrimPrefix(input, "/")
	prefix := ""
	if hasSlash {
		prefix = "/"
	}
	// State 2 / 3: we have a complete command head + maybe an arg.
	for _, head := range chubCommandHeads {
		hs := string(head)
		if bare == hs {
			args := m.chubCommandArgs(head)
			if len(args) == 0 {
				return input, false, 0
			}
			pick := args[cycleIdx%len(args)]
			return prefix + hs + " " + pick, true, len(args)
		}
		if strings.HasPrefix(bare, hs+" ") {
			argPartial := strings.TrimSpace(bare[len(hs)+1:])
			args := m.chubCommandArgs(head)
			matches := matchPrefixCI(args, argPartial)
			if len(matches) == 0 {
				return input, false, 0
			}
			return prefix + hs + " " + matches[cycleIdx%len(matches)],
				true, len(matches)
		}
	}
	// State 1: complete the command head itself.
	matches := []string{}
	for _, head := range chubCommandHeads {
		hs := string(head)
		if bare == "" || strings.HasPrefix(hs, bare) {
			matches = append(matches, hs)
		}
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return input, false, 0
	}
	return prefix + matches[cycleIdx%len(matches)], true, len(matches)
}

// matchPrefixCI returns items that start with prefix (case-insensitive),
// preserving original case in the output. Sorted alphabetically.
func matchPrefixCI(items []string, prefix string) []string {
	pl := strings.ToLower(prefix)
	out := []string{}
	for _, it := range items {
		if pl == "" || strings.HasPrefix(strings.ToLower(it), pl) {
			out = append(out, it)
		}
	}
	sort.Strings(out)
	return out
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
	case ChubCmdRefreshClaude, ChubCmdRestart:
		// :restart is a friendlier alias for /refresh-claude. Same
		// underlying RPC; chosen as a discoverable verb in the
		// rail palette so users can guess it without reading help.
		return m.doChubRefreshClaude()
	case ChubCmdClone:
		return m.doChubClone()
	case ChubCmdReleaseAll:
		return m.doChubReleaseAll(arg)
	case ChubCmdRun:
		return m.doChubRun(arg)
	case ChubCmdStopRun:
		return m.doChubStopRun(arg)
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

// nextCloneName picks the first free "<base>-N" name where N is 2,
// 3, 4, ... ensuring the new clone doesn't collide with the focused
// session itself or any other live session in the rail. ``base`` is
// the focused session's name with any existing ``-N`` suffix
// stripped, so cloning ``web-2`` produces ``web-3`` rather than
// ``web-2-2``.
func (m Model) nextCloneName(base string) string {
	// Trim a trailing "-N" so successive clones produce -2, -3, ...
	// rather than -2, -2-2, -2-2-2.
	root := base
	if i := strings.LastIndex(base, "-"); i > 0 {
		if n, err := strconv.Atoi(base[i+1:]); err == nil && n > 0 {
			root = base[:i]
		}
	}
	taken := make(map[string]bool, len(m.sessions))
	for _, s := range m.sessions {
		taken[s.Name] = true
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", root, n)
		if !taken[candidate] {
			return candidate
		}
	}
	// Fallback (a thousand clones with the same base — practically
	// impossible). Just append a high-N anyway.
	return fmt.Sprintf("%s-1000", root)
}

// doChubClone duplicates the focused session: same cwd, branch,
// tags, and folder. The new session's name is auto-suffixed
// (``-2``/``-3``/...) to avoid colliding with the original or any
// other live session — the daemon's UNIQUE constraint would
// otherwise reject the spawn. Folder assignment piggybacks on the
// existing doSpawn folder helper.
func (m Model) doChubClone() tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	newName := m.nextCloneName(s.Name)
	folder := m.folders.FolderForSession(s.ID)
	cwd := s.Cwd
	tags := append([]string(nil), s.Tags...)
	branch := ""
	// If the original was a worktree-backed session, derive a fresh
	// branch from the original's worktree path's terminal segment —
	// but cloning shouldn't reuse the same branch (that'd conflict
	// with the existing worktree). Default to NO branch on clone:
	// users can always /clone then /refresh on a branch. Keeping
	// the simple-version semantics from Phase 1.
	return m.doSpawn(newName, cwd, tags, folder, branch)
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

// doChubReleaseAll is the rail-palette equivalent of
// ``chubby release --all --yes``. Bare ``:release-all`` previews the
// count and refuses to fire; ``:release-all confirm`` runs full
// teardown on every non-dead, non-readonly session. Falls back to
// detach_session for sessions whose claude id hasn't bound yet
// (release_session refuses those because its result tuple needs
// the id).
func (m Model) doChubReleaseAll(arg string) tea.Cmd {
	type tgt struct{ id, name string }
	var targets []tgt
	for _, s := range m.sessions {
		if s.Status == StatusDead || s.Kind == KindReadonly {
			continue
		}
		targets = append(targets, tgt{id: s.ID, name: s.Name})
	}
	if strings.TrimSpace(arg) != "confirm" {
		n := len(targets)
		return func() tea.Msg {
			if n == 0 {
				return composeFailedMsg{fmt.Errorf("no live sessions to release")}
			}
			return composeFailedMsg{fmt.Errorf(
				"this will release %d session(s) — type ':release-all confirm' to proceed",
				n,
			)}
		}
	}
	if len(targets) == 0 {
		return func() tea.Msg {
			return composeFailedMsg{fmt.Errorf("no live sessions to release")}
		}
	}
	c := m.client
	return func() tea.Msg {
		ok, fail := 0, 0
		for _, t := range targets {
			if _, err := c.Call(context.Background(), "release_session",
				map[string]any{"id": t.id}); err != nil {
				// release_session refuses sessions without a bound
				// claude id; fall through to detach_session which
				// runs teardown + marks DEAD without needing the id.
				if _, err2 := c.Call(context.Background(), "detach_session",
					map[string]any{"id": t.id}); err2 != nil {
					fail++
					continue
				}
			}
			ok++
		}
		toast := fmt.Sprintf("released %d session(s)", ok)
		if fail > 0 {
			toast += fmt.Sprintf(", %d failed", fail)
		}
		return chubCommandDoneMsg{toast: toast}
	}
}

// doChubRun fires start_run_command for the focused session at the
// given run-array index. Long-running command (e.g. ``bun dev``)
// detaches from the daemon's process group and dies automatically
// when the session is released. Surfaces a toast with pid + log
// path so the user can ``tail -f`` it from another terminal.
//
// Index-only for v1: tab-completion of run-command names by content
// would require eagerly fetching list_run_commands per focused
// session, which is more plumbing than the feature warrants.
func (m Model) doChubRun(arg string) tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return func() tea.Msg {
			return composeFailedMsg{fmt.Errorf("no session focused")}
		}
	}
	sid := s.ID
	c := m.client
	return func() tea.Msg {
		idx, err := parseRunIndex(arg)
		if err != nil {
			return composeFailedMsg{err}
		}
		raw, err := c.Call(context.Background(), "start_run_command",
			map[string]any{"session_id": sid, "index": idx})
		if err != nil {
			return composeFailedMsg{err}
		}
		var r struct {
			Pid     int    `json:"pid"`
			LogPath string `json:"log_path"`
			Cmd     string `json:"cmd"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return composeFailedMsg{err}
		}
		return chubCommandDoneMsg{toast: fmt.Sprintf(
			"run %d started: %s (pid %d, log %s)",
			idx, r.Cmd, r.Pid, r.LogPath,
		)}
	}
}

// doChubStopRun stops a running ``run`` command for the focused
// session. No-op (chubCommandDoneMsg with an explanatory toast)
// when nothing's running at that index.
func (m Model) doChubStopRun(arg string) tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return func() tea.Msg {
			return composeFailedMsg{fmt.Errorf("no session focused")}
		}
	}
	sid := s.ID
	c := m.client
	return func() tea.Msg {
		idx, err := parseRunIndex(arg)
		if err != nil {
			return composeFailedMsg{err}
		}
		raw, err := c.Call(context.Background(), "stop_run_command",
			map[string]any{"session_id": sid, "index": idx})
		if err != nil {
			return composeFailedMsg{err}
		}
		var r struct {
			Stopped bool `json:"stopped"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return composeFailedMsg{err}
		}
		if !r.Stopped {
			return chubCommandDoneMsg{toast: fmt.Sprintf(
				"run %d wasn't running", idx,
			)}
		}
		return chubCommandDoneMsg{toast: fmt.Sprintf(
			"run %d stopped", idx,
		)}
	}
}

// parseRunIndex turns the ``:run`` argument into a non-negative int.
// Empty string is rejected with a usage error.
func parseRunIndex(arg string) (int, error) {
	s := strings.TrimSpace(arg)
	if s == "" {
		return 0, fmt.Errorf("usage: :run <index>  (0-based — see .chubby/config.json `run` array)")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("run index must be a non-negative integer; got %q", arg)
	}
	return n, nil
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
