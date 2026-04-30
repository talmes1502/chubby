// Package model is the Bubble Tea Model for chubby-tui: the top-level
// session-list rail, focused-viewport pane, compose bar, and event/refresh
// wiring. Modal panes (broadcast, grep, history) are layered on top and
// share Mode dispatch.
package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/USER/chubby/tui/internal/rpc"
	"github.com/USER/chubby/tui/internal/views"
)

// Turn is a single transcript entry — a user prompt or an assistant
// response. Text is already stripped of tool block boilerplate by the
// daemon: it's just the user-readable text plus compact tool summaries.
type Turn struct {
	Role string
	Text string
	Ts   int64
}

// turnsCap is the per-session retention cap. Beyond this we trim the
// oldest entries so the model stays bounded for long sessions.
const turnsCap = 500

// Session mirrors the SessionDict schema returned by chubbyd's list_sessions.
type Session struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Color  string   `json:"color"`
	Kind   string   `json:"kind"`
	Status string   `json:"status"`
	Cwd    string   `json:"cwd"`
	Tags   []string `json:"tags"`
}

// ActivePane identifies which of the two main-view panes (rail vs
// conversation) currently receives arrow / paging input when compose
// is empty. Tab toggles between them; Ctrl+Tab still exists as the
// power-user "cycle focused session directly" shortcut.
type ActivePane int

const (
	// PaneRail is the default — Up/Down walks the rail cursor,
	// PgUp/PgDn moves it by 5 rows, Enter focuses the cursored
	// session or toggles a folder header.
	PaneRail ActivePane = iota
	// PaneConversation routes arrow / paging keys to the per-session
	// scroll helpers.
	PaneConversation
)

// Mode controls which modal pane is on top of the main two-pane layout.
// Subsequent phases add ModeGrep, ModeHistory, ModeReconnecting.
type Mode int

const (
	ModeMain Mode = iota
	ModeBroadcast
	ModeGrep
	ModeHistory
	ModeReconnecting
	ModeSpawn
	ModeSearch
	ModeHelp
	ModeRename
	ModeAttach
	ModeNewFolder
	// ModeEditor: the right-side file viewer pane is taking input —
	// either a path-prompt textinput (Ctrl+O just pressed) or
	// scroll/external-open commands. The "pane is visible" bit is held
	// independently in editorState.visible: a user can leave ModeEditor
	// (Esc back to ModeMain) while the pane stays on-screen.
	ModeEditor
)

// renameTarget says whether ModeRename is editing a session name or a
// group label (the first tag across a set of sessions).
type renameTarget int

const (
	RenameSession renameTarget = iota
	RenameGroup
	// RenameFolder edits a user-created folder name (Phase A). Distinct
	// from RenameGroup because folders are not tags — the rename
	// rewrites folders.json instead of issuing per-session
	// set_session_tags RPCs.
	RenameFolder
)

// renameState backs ModeRename: a single textinput plus the ids the
// rename applies to. For RenameSession that's a single session id; for
// RenameGroup it's every session whose first tag is the old group
// name. oldName is what we display in the modal header.
type renameState struct {
	input    textinput.Model
	target   renameTarget
	sessions []string
	oldName  string
}

// newFolderState backs ModeNewFolder: a single textinput plus an err
// slot so duplicate-name rejection can surface inline without dropping
// the user back to the main view.
type newFolderState struct {
	input textinput.Model
	err   error
}

// broadcastState is held in the Model so the reducer can mutate it
// cleanly. Tab cycles fields {0=list,1=text,2=send}; space toggles a
// session checkbox; Enter on Send fires inject for each selected id.
type broadcastState struct {
	selected  map[string]bool
	field     int
	cursor    int
	text      string
	compIndex int    // slash-command Tab cycle position
	compLast  string // last value produced by a slash-tab; used to detect edits
}

// grepState backs the FTS palette: a textinput plus a debounced
// search command pipeline. debounceToken invalidates stale results.
type grepState struct {
	query         textinput.Model
	results       []map[string]any
	cursor        int
	debounceToken int
}

// toast is an awaiting_user pop-up shown bottom-right for ttl.
type toast struct {
	sessionName string
	color       string
	expiresAt   time.Time
}

// searchState backs ModeSearch: a small filter bar over the session
// rail. The query persists even after returning to ModeMain so the
// rail keeps its filter; Esc clears it.
type searchState struct {
	query textinput.Model
}

// spawnState backs ModeSpawn: a small modal with three textinputs
// (name + cwd + folder). Tab cycles between the fields. cwd defaults
// to the focused session's cwd or $HOME and supports ~ expansion at
// submit. folder is optional; when non-empty, the spawn handler
// assigns the freshly-created session into that TUI-side folder
// (creating it if it doesn't exist) — the daemon's spawn_session RPC
// is invoked with empty tags regardless. Folders are TUI-local state.
type spawnState struct {
	name   textinput.Model
	cwd    textinput.Model
	folder textinput.Model
	field  int // 0=name, 1=cwd, 2=folder
	err    error

	// Tab path completion in the cwd field (Phase B).
	// pathCompletionIndex advances on each repeated Tab; pathCompletionLast
	// is the last value Tab produced — if the user has edited the field
	// since, we reset the cycle so a fresh prefix re-anchors at idx 0.
	pathCompletionIndex int
	pathCompletionLast  string

	// Ctrl+P recent cwds picker (Phase B).
	// recentCwds is fetched lazily on first Ctrl+P and cached for the
	// lifetime of the modal. recentCwdIndex cycles through them; we
	// don't reset it on edits because the user explicitly opted into the
	// list — typing in between Ctrl+P presses still keeps the cycle.
	recentCwds       []string
	recentCwdIndex   int
	recentCwdsLoaded bool
}

// attachState backs ModeAttach: a multi-select picker over the
// daemon's scan_candidates RPC results. selected maps a candidate
// index to whether it's currently checked. notice surfaces transient
// post-attach feedback ("attached N sessions", or "select at least
// one with Space"); err captures scan-time RPC failures.
//
// confirmDetachN is set when the user presses 'd' to start a bulk
// detach: any further 'y'/'Y' confirms and fires release_session for
// every selected already-attached candidate. 'n'/'N'/Esc cancels. The
// number is purely for the prompt copy ("Detach N sessions? (y/n)").
type attachState struct {
	candidates     []map[string]any // raw rows from scan_candidates RPC
	selected       map[int]bool     // candidate index → selected
	cursor         int
	loading        bool
	err            error
	notice         string // post-attach feedback like "attached 3 sessions"
	confirmDetachN int    // >0 = pending 'd' confirm prompt
}

// editorState backs the right-side file viewer pane (Phase C). The
// pane's visibility is independent of the active Mode: it stays on
// screen even after the user Escs back to ModeMain so the file is
// still visible while they read the conversation. ModeEditor controls
// where keys go (path prompt or scroll/open commands); editor.visible
// controls whether the pane occupies its own column at render time.
//
// pathInput is the textinput shown when inPathPrompt is true (Ctrl+O
// just pressed and the user hasn't submitted yet). After submit we set
// inPathPrompt=false and the pane displays the file with scroll bound
// to scrollOffset.
//
// content is the raw bytes (capped at editorMaxBytes); highlighted is
// the chroma-rendered ANSI string. We cache the highlighted string per
// load so re-renders on every tick don't re-tokenise.
//
// recentPaths is the deduped, most-recent-first set of paths detected
// in conversation rendering — Ctrl+] opens the head entry.
type editorState struct {
	visible      bool
	path         string
	content      string
	highlighted  string
	lang         string
	scrollOffset int
	err          error
	truncated    bool
	pathInput    textinput.Model
	inPathPrompt bool
	recentPaths  []string
}

// editorMaxBytes is the per-file load cap. Anything larger is loaded
// up to this many bytes and a "(truncated — open externally)" note is
// shown in the title; we still render the prefix so the user gets
// something useful for huge log files.
const editorMaxBytes = 2 * 1024 * 1024

// editorRecentPathsCap caps the recent-paths slice. The Ctrl+]
// shortcut only ever consumes the head entry; we keep more for
// debugging / future palette use.
const editorRecentPathsCap = 50

// historyState backs the three-column miller view: hub-runs on the
// left, sessions in the selected run in the middle, and the log
// preview on the right. Tab cycles columns.
type historyState struct {
	runs        []map[string]any
	runCursor   int
	runSessions []Session
	sessCursor  int
	column      int // 0=runs, 1=sessions, 2=preview
	preview     string
}

// Model is the Bubble Tea state.
type Model struct {
	client   *rpc.Client
	sessions []Session
	focused  int
	// conversation holds the structured transcript per session id, fed by
	// transcript_message events from the daemon. This replaces the old raw
	// PTY byte buffer (m.output): the previous viewport was unreadable
	// because Bubble Tea's lipgloss frame doesn't honor cursor positioning
	// escapes that Claude emits constantly.
	conversation map[string][]Turn
	width        int
	height       int
	err          error

	mode    Mode
	compose textinput.Model

	bcast     broadcastState
	grep      grepState
	history   historyState
	spawn     spawnState
	search    searchState
	rename    renameState
	attach    attachState
	newFolder newFolderState
	editor    editorState

	toasts []toast

	reconnectAttempts int

	// groupCollapsed tracks which group headers are collapsed in the
	// left rail. Persisted to ~/.claude/hub/tui-state.json on change.
	// Keys may be either auto-group names or user-folder names — they
	// share the same map so the same Space-toggle code works for both.
	groupCollapsed map[string]bool
	// folders is the user-created folder state (Phase A). Persisted to
	// ~/.claude/chubby/folders.json. Sessions assigned to a folder
	// render under it; unassigned sessions fall through to the legacy
	// auto-grouping logic.
	folders FoldersState
	// railCollapsed hides the left rail entirely; viewport takes full
	// width. Toggled with Ctrl+J. Persisted in tui-state.json.
	railCollapsed bool
	// railCursor indexes the currently-highlighted row in the visible
	// rail (may be a group header or a session). Up/Down walks this;
	// Tab/Shift+Tab walks m.focused (session-only).
	railCursor int
	// activePane decides where compose-empty arrow / paging keys go
	// (D8). Tab toggles between PaneRail (default) and
	// PaneConversation. The visual cue is the focused pane's border
	// color in renderList / renderViewport.
	activePane ActivePane

	// completion tracks @-name autocomplete state in the compose bar.
	completionIndex   int    // which match in the cycle is active
	completionPartial string // the partial we last completed for, to detect input change

	// slashCompletionLast is the compose value produced by the most
	// recent slash-completion Tab. If the user edits past that point,
	// the next Tab restarts the cycle from 0.
	slashCompletionLast string

	// slashPopup state — non-nil/non-empty when the compose bar starts
	// with "/" and there are matching commands. Up/Down moves cursor;
	// Enter accepts; Esc closes. Recomputed on every compose mutation.
	slashPopupCursor int
	slashPopupCmds   []views.SlashCommand

	// historyLoaded tracks which session ids we've already requested
	// transcript history for, so we only fire the RPC once per session
	// per TUI session. Without this guard the per-tick refresh would
	// re-load history every 2 seconds.
	historyLoaded map[string]bool

	// initialListReceived flips true after the first listMsg arrives.
	// Used to auto-open the spawn modal when the TUI starts and there
	// are no sessions to focus.
	initialListReceived bool

	// spinnerFrame indexes into spinnerRunes; advanced ~120ms by
	// spinnerTickMsg while at least one session is "thinking". When all
	// sessions are idle, the tick stops re-arming itself to save CPU,
	// and the next listMsg/evMsg restarts it if a session flips back
	// into thinking.
	spinnerFrame int
	// spinnerRunning tracks whether a spinner tick is currently
	// scheduled. Without this guard, every listMsg/evMsg that sees a
	// thinking session would queue a fresh tick — multiplying the
	// frame advance rate the user actually sees.
	spinnerRunning bool

	// scrollOffset is the number of lines scrolled UP from the bottom
	// of the conversation, per session. 0 = pinned to bottom. Larger
	// values = scrolled toward older messages. Per-session state
	// preserves the user's reading position when switching focus.
	scrollOffset map[string]int

	// newSinceScroll counts turns appended since the user last left
	// the bottom of THIS session's conversation. Drives the "↓ N new"
	// indicator overlay. Reset to 0 whenever the user is back at the
	// bottom (scrollOffset[sid] == 0).
	newSinceScroll map[string]int

	// lastViewportInnerW / lastViewportInnerH cache the last rendered
	// viewport's inner dimensions (after subtracting the rounded
	// border). renderViewport sets these as a side-effect; the
	// scroll-clamping helpers read them so a key event arriving before
	// the next render uses the most recent geometry. Without this we'd
	// either re-render on every keystroke just to compute max-scroll
	// or risk clamping against stale dimensions.
	lastViewportInnerW int
	lastViewportInnerH int

	// lastViewportLineCount caches the wrapped-line count of the
	// focused conversation as last rendered. Used by maxScrollFor to
	// answer "how far up can the user scroll?" without re-running the
	// full lipgloss wrap. Refreshed every render.
	lastViewportLineCount int

	// lastUsage records the latest token usage for each session,
	// driven by session_usage_changed events. The samples ring (last
	// 10 entries) feeds the tokens/sec calculation that powers the
	// thinking-banner activity slider and "almost done" status.
	lastUsage map[string]sessionUsage

	// thinkingStartedAt remembers when each session most recently
	// flipped to "thinking". The banner's elapsed counter and rotating
	// status text both read this value; the entry is cleared on any
	// non-thinking status change so the timer resets cleanly between
	// turns.
	thinkingStartedAt map[string]time.Time

	// startupFocusName, when non-empty, is the session name passed via
	// CHUBBY_FOCUS_SESSION (i.e. ``chubby tui --focus <name>``).
	// Resolved on the FIRST listMsg by scanning m.sessions for a Name
	// match, then cleared so later list refreshes don't re-snap the
	// user's focus. Pairs with CHUBBY_DETACHED=1 (which sets
	// railCollapsed). Still supported as a manual flag, even though
	// /detach no longer uses it (see doChubDetach for the new
	// release-session semantics).
	startupFocusName string
}

// sessionUsage holds the running token totals + rolling samples for
// one session. samples is bounded at 10 entries — enough to span the
// 2-second tokens/sec window at typical event rates without growing
// without bound on long sessions.
type sessionUsage struct {
	InputTokens          int
	OutputTokens         int
	CacheReadInputTokens int
	LastUpdate           time.Time
	samples              []usageSample
}

// usageSample mirrors views.UsageSample but lives in the model
// package so we can keep the field-typed map without introducing a
// circular import. We convert to []views.UsageSample at render time.
type usageSample struct {
	Ts           time.Time
	OutputTokens int
}

type tickMsg struct{}
type spinnerTickMsg struct{}
type evMsg rpc.Event
type listMsg []Session
type errMsg struct{ err error }
type composeSentMsg struct{}
type composeFailedMsg struct{ err error }

// chubCommandDoneMsg is emitted by the chubby-side compose-bar commands
// (/color, /rename, /tag, /refresh-claude) after a successful RPC. The
// reducer clears the compose value and triggers a session refresh so
// any color/name/tag change propagates to the rail. ``toast``, when
// non-empty, surfaces a short transient message (e.g. "refreshing api…").
type chubCommandDoneMsg struct{ toast string }
type bcastDoneMsg struct{ n int }
type grepDebounceMsg struct{ token int }
type grepResultsMsg struct {
	token   int
	matches []map[string]any
}
type historyRunsMsg []map[string]any
type historyRunSessionsMsg struct {
	sessions []Session
	preview  string
}
type toastTickMsg struct{}
type reconnectAttemptMsg struct{}
type reconnectedMsg struct{}
type spawnDoneMsg struct{}
type spawnFailedMsg struct{ err error }

// spawnRecentCwdsLoadedMsg lands when the spawn modal's first Ctrl+P
// finishes its recent_cwds RPC. We cache the list on the modal and set
// the cwd field to the first entry. Subsequent Ctrl+P presses don't
// re-issue the RPC — they just advance through the cached slice.
type spawnRecentCwdsLoadedMsg struct {
	cwds []string
	err  error
}
type renameDoneMsg struct{}
type respawnDoneMsg struct{}
type historyTurnsMsg struct {
	sid   string
	turns []Turn
}
type copiedMsg struct{ count int }

// editorFileLoadedMsg lands when an asynchronous file-read completes.
// path is the absolute path the user typed (resolved); content is the
// raw bytes (already truncated to editorMaxBytes by the loader if
// applicable); highlighted/lang come from views.HighlightFile;
// truncated reports whether the file was capped. err non-nil means the
// read failed (file not found, permission denied, etc.) — the reducer
// surfaces it inline in the editor pane and stays in inPathPrompt=true
// so the user can fix the path.
type editorFileLoadedMsg struct {
	path         string
	content      string
	highlighted  string
	lang         string
	truncated    bool
	err          error
	scrollOffset int // initial scroll position; non-zero when path had ":<line>"
}

// autoSpawnedMsg is emitted when the empty-startup auto-spawn succeeds.
// The reducer surfaces a transient toast and triggers a session refresh.
type autoSpawnedMsg struct{ name, cwd string }

// autoSpawnFallbackMsg is emitted when auto-spawn cannot reasonably
// succeed (HOME unresolvable, every "temp"/"temp-N" already taken,
// non-name-collision RPC error). The reducer falls back to opening the
// spawn modal so the user can pick something else.
type autoSpawnFallbackMsg struct{ err error }

// attachScannedMsg carries the result of the scan_candidates RPC kicked
// off when ModeAttach opens (or on rescan via 'r'). err is the
// transport-level error; candidates is the raw map slice from the
// JSON-RPC reply (we keep the dynamic shape so extra daemon-side
// fields surface untouched).
type attachScannedMsg struct {
	candidates []map[string]any
	err        error
}

// attachDoneMsg is emitted after a multi-select picker action — either
// attach (Enter) or detach ('d' + 'y'). n is the number of successful
// operations; detached=true means this was a release_session pass
// rather than an attach pass (drives the toast wording). err captures
// the last error seen during the loop.
type attachDoneMsg struct {
	n        int
	err      error
	detached bool
}

// New constructs a Model bound to an already-connected rpc.Client.
//
// Honours two env vars set by `chubby tui --focus <name> --detached`
// (a manual single-session view; the /detach slash command no longer
// goes through this path — it now releases the session and opens a
// real `claude --resume` outside chubby):
//   - CHUBBY_FOCUS_SESSION=<name>: stash a startup-focus name resolved
//     against the first listMsg's session slice.
//   - CHUBBY_DETACHED=1: collapse the rail at startup so the detached
//     window opens straight into a single-session view.
func New(c *rpc.Client) Model {
	railCollapsed := LoadRailCollapsed()
	if os.Getenv("CHUBBY_DETACHED") == "1" {
		railCollapsed = true
	}
	return Model{
		client:         c,
		conversation:   map[string][]Turn{},
		mode:           ModeMain,
		compose:        views.NewCompose(),
		bcast:          broadcastState{selected: map[string]bool{}},
		grep:           grepState{query: views.NewGrepQuery()},
		groupCollapsed: LoadCollapsedGroups(),
		railCollapsed:  railCollapsed,
		folders:        LoadFolders(),
		search:         searchState{query: views.NewSearchQuery()},
		historyLoaded:     map[string]bool{},
		scrollOffset:      map[string]int{},
		newSinceScroll:    map[string]int{},
		lastUsage:         map[string]sessionUsage{},
		thinkingStartedAt: map[string]time.Time{},
		startupFocusName:  os.Getenv("CHUBBY_FOCUS_SESSION"),
	}
}

// filterSessions returns sessions whose Name contains q (case-insensitive
// substring). An empty q returns the input slice unchanged.
func filterSessions(sessions []Session, q string) []Session {
	if q == "" {
		return sessions
	}
	ql := strings.ToLower(q)
	out := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		if strings.Contains(strings.ToLower(s.Name), ql) {
			out = append(out, s)
		}
	}
	return out
}

// Init kicks off initial session refresh, event listening, and the
// background tickers (refresh, toast TTL, and spinner). The spinner
// tick self-arms only while a session is thinking — see spinnerTickMsg
// in Update — so kicking it off here is cheap even when nothing is.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshSessions(), m.listenEvents(), tickEvery(), toastTick(), spinnerTick())
}

func (m Model) refreshSessions() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "list_sessions", nil)
		if err != nil {
			return errMsg{err}
		}
		var r struct {
			Sessions []Session `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return errMsg{err}
		}
		return listMsg(r.Sessions)
	}
}

// loadHistory fetches the bound JSONL turns for a session via the daemon
// and feeds them into the conversation map. Soft-fails on RPC error so
// the viewport just stays empty until live events arrive — there's no
// useful UI for "history fetch failed."
func (m Model) loadHistory(sid string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "get_session_history",
			map[string]any{"session_id": sid, "limit": 500})
		if err != nil {
			return nil
		}
		var r struct {
			Turns []struct {
				Role string `json:"role"`
				Text string `json:"text"`
				Ts   int64  `json:"ts"`
			} `json:"turns"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil
		}
		turns := make([]Turn, 0, len(r.Turns))
		for _, t := range r.Turns {
			turns = append(turns, Turn{Role: t.Role, Text: t.Text, Ts: t.Ts})
		}
		return historyTurnsMsg{sid: sid, turns: turns}
	}
}

func (m Model) listenEvents() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				return reconnectAttemptMsg{}
			}
			return evMsg(ev)
		case <-c.Disconnected():
			return reconnectAttemptMsg{}
		}
	}
}

func tickEvery() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// toastTick fires every 500ms; the reducer drops expired toasts and
// re-arms the ticker.
func toastTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return toastTickMsg{} })
}

// spinnerTick fires every 120ms; the reducer advances spinnerFrame and
// re-arms the ticker only while at least one session is thinking. When
// all sessions are idle the tick stops; the next status flip back to
// thinking restarts it via listMsg/evMsg.
func spinnerTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// anyThinking reports whether any session is currently in the thinking
// state — used by the reducer to decide whether to keep the spinner
// tick running and by listMsg/evMsg to (re)start it on demand.
func (m Model) anyThinking() bool {
	for _, s := range m.sessions {
		if s.Status == "thinking" {
			return true
		}
	}
	return false
}

// Update is the Bubble Tea reducer.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Refresh the cached viewport geometry so scroll-clamping works
		// the moment the user starts pressing keys after a resize. The
		// math here mirrors viewMain's layout decisions; we don't run
		// the full layout but we do compute the same convoW/h the
		// renderer will use, so maxScrollFor sees correct numbers.
		m.recomputeViewportGeom()
		// Resize that shrinks the conversation pane may have stranded
		// per-session scrollOffsets past the new max — clamp them so
		// the next render doesn't show empty space above the banner.
		m.clampAllScrollOffsets()
		return m, nil
	case listMsg:
		m.sessions = []Session(msg)
		if m.focused >= len(m.sessions) {
			m.focused = 0
		}
		// `chubby tui --focus <name>` passes CHUBBY_FOCUS_SESSION=<name>;
		// we resolve it on the first list because it's the first time
		// we have the name->index mapping. We clear the field so a later refresh
		// (e.g. a rename) doesn't yank focus away from wherever the
		// user has since moved it.
		if m.startupFocusName != "" {
			for i, s := range m.sessions {
				if s.Name == m.startupFocusName {
					m.focused = i
					break
				}
			}
			m.startupFocusName = ""
		}
		m.syncRailCursorToFocus()
		// First time we see each session, lazily load its prior transcript
		// from the daemon so the viewport stays populated across `chubby down`
		// + `chubby up` cycles. Live transcript_message events still append
		// on top of whatever we load here.
		cmds := []tea.Cmd{m.listenEvents()}
		for _, s := range m.sessions {
			if !m.historyLoaded[s.ID] {
				m.historyLoaded[s.ID] = true
				cmds = append(cmds, m.loadHistory(s.ID))
			}
		}
		// First listMsg + no sessions: auto-spawn a "temp" session at
		// $HOME so the user has something to chat with immediately. The
		// modal only appears as a fallback when auto-spawn fails (e.g.
		// a leftover "temp" name collision the variant cycle can't escape).
		if !m.initialListReceived {
			m.initialListReceived = true
			if len(m.sessions) == 0 && m.mode == ModeMain {
				cmds = append(cmds, m.autoSpawnDefault())
			}
			// One-time D10c migration: map legacy auto-grouped sessions
			// (first tag / cwd basename) into explicit folders so users
			// don't lose their cluster layout when the rail flattens.
			// Sentinel-guarded inside the helper so subsequent launches
			// no-op even if the user never restarts the TUI.
			cmds = append(cmds, runMigrationCmd(m.sessions, m.folders))
		}
		// If a session is now thinking but the spinner tick has gone
		// quiet (because nothing was thinking last tick), re-arm it so
		// the rail glyph actually animates.
		if m.anyThinking() && !m.spinnerRunning {
			m.spinnerRunning = true
			cmds = append(cmds, spinnerTick())
		}
		return m, tea.Batch(cmds...)
	case evMsg:
		ev := rpc.Event(msg)
		if ev.Method == "event" {
			subM, _ := ev.Params["event_method"].(string)
			subP, _ := ev.Params["event_params"].(map[string]any)
			switch subM {
			case "transcript_message":
				sid, _ := subP["session_id"].(string)
				role, _ := subP["role"].(string)
				text, _ := subP["text"].(string)
				ts, _ := subP["ts"].(float64)
				// Snapshot length before append so we can tell whether
				// the dedup actually skipped the turn — only "real"
				// (non-deduped) appends should bump newSinceScroll, or
				// the unread badge will inflate from tailer replays.
				prevLen := len(m.conversation[sid])
				// appendTranscriptTurn dedup-skips entries that match the
				// last 5 turns (role+text). The daemon's tailer can replay
				// JSONL turns from offset 0 immediately after we've
				// canonically seeded the conversation via
				// get_session_history; without dedup the user sees each
				// recent turn twice.
				m.appendTranscriptTurn(sid, role, text, int64(ts))
				appended := len(m.conversation[sid]) > prevLen
				// D7: when the user is scrolled UP from the bottom,
				// preserve their reading position and instead increment
				// the unread counter so they see "↓ N new" guidance.
				// When pinned to bottom (offset==0), do nothing — the
				// renderer naturally tails the latest line.
				if appended && m.scrollOffset[sid] > 0 {
					m.newSinceScroll[sid]++
				}
				// Auto-detect file paths in the new turn so Ctrl+] can
				// open the most-recent mention without re-rendering.
				m.harvestPathsFromText(text)
			case "session_usage_changed":
				sid, _ := subP["session_id"].(string)
				inF, _ := subP["input_tokens"].(float64)
				outF, _ := subP["output_tokens"].(float64)
				cacheF, _ := subP["cache_read_input_tokens"].(float64)
				if sid != "" {
					cur := m.lastUsage[sid]
					cur.InputTokens = int(inF)
					cur.OutputTokens = int(outF)
					cur.CacheReadInputTokens = int(cacheF)
					cur.LastUpdate = time.Now()
					cur.samples = append(cur.samples, usageSample{
						Ts:           time.Now(),
						OutputTokens: int(outF),
					})
					if len(cur.samples) > 10 {
						cur.samples = cur.samples[len(cur.samples)-10:]
					}
					m.lastUsage[sid] = cur
				}
			case "session_status_changed":
				sid, _ := subP["session_id"].(string)
				newStatus, _ := subP["status"].(string)
				// Track when a session enters thinking so the banner
				// can show elapsed time. Clear on any other status
				// transition so the timer resets between turns.
				if sid != "" {
					if newStatus == "thinking" {
						m.thinkingStartedAt[sid] = time.Now()
					} else {
						delete(m.thinkingStartedAt, sid)
					}
				}
				if newStatus == "awaiting_user" {
					focusedSid := ""
					if s := m.focusedSession(); s != nil {
						focusedSid = s.ID
					}
					if sid != focusedSid {
						for _, s := range m.sessions {
							if s.ID == sid {
								m.toasts = append(m.toasts, toast{
									sessionName: s.Name,
									color:       s.Color,
									expiresAt:   time.Now().Add(5 * time.Second),
								})
								break
							}
						}
					}
				}
				return m, tea.Batch(m.refreshSessions(), m.listenEvents())
			case "session_added", "session_renamed",
				"session_recolored", "session_tagged":
				return m, tea.Batch(m.refreshSessions(), m.listenEvents())
			case "session_id_resolved":
				// The daemon just bound a JSONL to this session — the
				// FIRST listMsg arrived before any JSONL existed, so the
				// initial loadHistory returned empty. Re-fetch now.
				sid, _ := subP["session_id"].(string)
				if sid != "" {
					m.historyLoaded[sid] = true
					return m, tea.Batch(m.refreshSessions(), m.listenEvents(), m.loadHistory(sid))
				}
			}
		}
		return m, m.listenEvents()
	case toastTickMsg:
		now := time.Now()
		kept := m.toasts[:0]
		for _, t := range m.toasts {
			if t.expiresAt.After(now) {
				kept = append(kept, t)
			}
		}
		m.toasts = kept
		return m, toastTick()
	case spinnerTickMsg:
		m.spinnerFrame++
		// Only re-tick while at least one session is thinking; idle
		// terminals shouldn't burn a 120ms wakeup forever. listMsg /
		// session_status_changed restart the tick if a session flips
		// back into thinking.
		if m.anyThinking() {
			m.spinnerRunning = true
			return m, spinnerTick()
		}
		m.spinnerRunning = false
		return m, nil
	case reconnectAttemptMsg:
		return m.attemptReconnect()
	case reconnectedMsg:
		m.mode = ModeMain
		m.reconnectAttempts = 0
		return m, tea.Batch(m.refreshSessions(), m.listenEvents())
	case tickMsg:
		return m, tea.Batch(m.refreshSessions(), tickEvery())
	case errMsg:
		m.err = msg.err
		return m, nil
	case composeSentMsg:
		m.compose.SetValue("")
		return m, nil
	case chubCommandDoneMsg:
		// Chub-side commands (/color, /rename, /tag, /refresh-claude,
		// /movetofolder, /removefromfolder) need a refresh so the new
		// color/name/tags/folder-membership propagates to the rail
		// immediately. Folders state lives entirely in the TUI — re-
		// load it from disk so the rail reflects the freshly written
		// folders.json without going through the daemon. Any
		// non-empty toast is surfaced via the same transient message
		// bubble we use for awaiting_user notifications.
		m.compose.SetValue("")
		m.folders = LoadFolders()
		if msg.toast != "" {
			m.toasts = append(m.toasts, toast{
				sessionName: msg.toast,
				color:       "10",
				expiresAt:   time.Now().Add(2 * time.Second),
			})
		}
		return m, m.refreshSessions()
	case composeFailedMsg:
		m.err = msg.err
		return m, nil
	case bcastDoneMsg:
		m.mode = ModeMain
		return m, m.refreshSessions()
	case spawnDoneMsg:
		m.mode = ModeMain
		return m, m.refreshSessions()
	case MigrationDoneMsg:
		// Reload folders so any newly-assigned sessions show up in the
		// rail immediately. Surface a transient toast only when the
		// migration actually moved at least one session — silence on
		// no-op (idempotent re-runs land here too via the sentinel
		// short-circuit, returning N=0).
		m.folders = LoadFolders()
		if msg.N > 0 {
			m.toasts = append(m.toasts, toast{
				sessionName: fmt.Sprintf("migrated %d sessions into folders", msg.N),
				color:       "10",
				expiresAt:   time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	case spawnFailedMsg:
		m.spawn.err = msg.err
		return m, nil
	case spawnRecentCwdsLoadedMsg:
		// Stay in the spawn modal regardless. If the RPC failed or the
		// list is empty, surface it inline; the user can dismiss with
		// Esc or just keep typing.
		if msg.err != nil {
			m.spawn.err = msg.err
			return m, nil
		}
		if len(msg.cwds) == 0 {
			m.spawn.err = fmt.Errorf("no recent cwds yet — type a path or Tab to complete")
			return m, nil
		}
		m.spawn.err = nil
		// First load: cache + jump to entry 0. Subsequent presses
		// already had the list cached, so we advance the index here.
		if !m.spawn.recentCwdsLoaded {
			m.spawn.recentCwds = msg.cwds
			m.spawn.recentCwdsLoaded = true
			m.spawn.recentCwdIndex = 0
		} else {
			m.spawn.recentCwdIndex = (m.spawn.recentCwdIndex + 1) % len(m.spawn.recentCwds)
		}
		picked := m.spawn.recentCwds[m.spawn.recentCwdIndex]
		m.spawn.cwd.SetValue(picked)
		m.spawn.cwd.SetCursor(len(picked))
		return m, nil
	case renameDoneMsg:
		m.mode = ModeMain
		// Folder renames live in folders.json — reload so the rail
		// picks up the new key. Cheap; safe for non-folder renames
		// (the on-disk shape is unchanged).
		m.folders = LoadFolders()
		return m, m.refreshSessions()
	case renameFolderFailedMsg:
		// Folder rename errors (collision, empty name) are recoverable
		// — return to main and surface the message via the same toast
		// channel awaiting_user notifications use, so the user sees
		// what went wrong without being trapped in a fatal-error
		// screen.
		m.mode = ModeMain
		m.toasts = append(m.toasts, toast{
			sessionName: "rename failed: " + msg.err.Error(),
			color:       "9",
			expiresAt:   time.Now().Add(3 * time.Second),
		})
		return m, nil
	case respawnDoneMsg:
		return m, m.refreshSessions()
	case historyTurnsMsg:
		// Replace any partially-live conversation with the loaded history.
		// If live events arrived during the load, they're discarded — the
		// JSONL is the canonical source. New live events after this will
		// append normally.
		m.conversation[msg.sid] = msg.turns
		// Seed recentPaths from the loaded transcript so Ctrl+] works
		// even before any live messages land.
		for _, p := range extractPathsFromTurns(msg.turns) {
			m.editor.recentPaths = pushRecentPath(m.editor.recentPaths, p)
		}
		return m, nil
	case editorFileLoadedMsg:
		if msg.err != nil {
			m.editor.err = msg.err
			// Stay in path-prompt so the user can edit the path and try
			// again — the visible pane shows the error inline.
			m.editor.visible = true
			m.editor.inPathPrompt = true
			m.mode = ModeEditor
			return m, nil
		}
		m.editor.err = nil
		m.editor.path = msg.path
		m.editor.content = msg.content
		m.editor.highlighted = msg.highlighted
		m.editor.lang = msg.lang
		m.editor.truncated = msg.truncated
		m.editor.scrollOffset = msg.scrollOffset
		m.editor.inPathPrompt = false
		m.editor.visible = true
		m.mode = ModeEditor
		// Record the path in the recent-paths slice (most-recent-first,
		// deduped) so Ctrl+] can re-open it later. We do this for every
		// successful load so manually-typed paths are also remembered.
		m.editor.recentPaths = pushRecentPath(m.editor.recentPaths, msg.path)
		return m, nil
	case copiedMsg:
		// Reuse the toast mechanism (the awaiting_user popup) for a brief
		// "copied N messages" confirmation. Bright green to distinguish
		// from the awaiting_user toasts which use the session's color.
		m.toasts = append(m.toasts, toast{
			sessionName: fmt.Sprintf("copied %d messages", msg.count),
			color:       "10",
			expiresAt:   time.Now().Add(2 * time.Second),
		})
		return m, nil
	case autoSpawnedMsg:
		// Surface a transient toast so the user understands what just
		// happened — they didn't ask for a session, but one appeared, so
		// hint at the rename shortcut for renaming it later.
		m.toasts = append(m.toasts, toast{
			sessionName: fmt.Sprintf("auto-started '%s' at %s (Ctrl+R to rename)",
				msg.name, prettyHomePath(msg.cwd)),
			color:     "10",
			expiresAt: time.Now().Add(5 * time.Second),
		})
		return m, m.refreshSessions()
	case autoSpawnFallbackMsg:
		// Auto-spawn couldn't proceed — punt to the modal so the user
		// can pick a name/cwd themselves.
		m.openSpawnModal()
		if msg.err != nil {
			m.spawn.err = msg.err
		}
		return m, nil
	case attachScannedMsg:
		m.attach.loading = false
		m.attach.err = msg.err
		m.attach.candidates = msg.candidates
		if m.attach.cursor >= len(m.attach.candidates) {
			m.attach.cursor = 0
		}
		return m, nil
	case attachDoneMsg:
		if msg.err != nil {
			m.attach.err = msg.err
			return m, nil
		}
		verb := "attached"
		if msg.detached {
			verb = "detached"
		}
		m.attach.notice = fmt.Sprintf("%s %d sessions", verb, msg.n)
		m.mode = ModeMain
		return m, m.refreshSessions()
	case grepDebounceMsg:
		if msg.token != m.grep.debounceToken {
			return m, nil
		}
		return m, m.doGrep(m.grep.query.Value(), msg.token)
	case grepResultsMsg:
		if msg.token != m.grep.debounceToken {
			return m, nil
		}
		m.grep.results = msg.matches
		if m.grep.cursor >= len(m.grep.results) {
			m.grep.cursor = 0
		}
		return m, nil
	case historyRunsMsg:
		m.history.runs = []map[string]any(msg)
		if len(m.history.runs) > 0 {
			return m, m.loadHubRun(m.history.runs[0])
		}
		return m, nil
	case historyRunSessionsMsg:
		m.history.runSessions = msg.sessions
		m.history.preview = msg.preview
		if m.history.sessCursor >= len(m.history.runSessions) {
			m.history.sessCursor = 0
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case ModeBroadcast:
		return m.handleKeyBroadcast(msg)
	case ModeGrep:
		return m.handleKeyGrep(msg)
	case ModeHistory:
		return m.handleKeyHistory(msg)
	case ModeSpawn:
		return m.handleKeySpawn(msg)
	case ModeRename:
		return m.handleKeyRename(msg)
	case ModeNewFolder:
		return m.handleKeyNewFolder(msg)
	case ModeSearch:
		return m.handleKeySearch(msg)
	case ModeAttach:
		return m.handleKeyAttach(msg)
	case ModeEditor:
		return m.handleKeyEditor(msg)
	case ModeHelp:
		// Any key dismisses the overlay.
		m.mode = ModeMain
		return m, nil
	case ModeReconnecting:
		// Swallow keys while we're reconnecting; user can still ctrl+c
		// because that's handled before the dispatch.
		return m, nil
	default:
		return m.handleKeyMain(msg)
	}
}

func (m Model) handleKeyMain(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Slash popup gets first dibs on navigation/accept/dismiss keys
	// when it's visible. We only intercept Ctrl+N/Ctrl+P here (which
	// would otherwise open the spawn modal / respawn) so the popup can
	// be driven keyboard-only without mouse-only fallbacks.
	if m.slashPopupVisible() {
		switch msg.String() {
		case "up", "ctrl+p":
			if m.slashPopupCursor > 0 {
				m.slashPopupCursor--
			}
			return m, nil
		case "down", "ctrl+n":
			if m.slashPopupCursor < len(m.slashPopupCmds)-1 {
				m.slashPopupCursor++
			}
			return m, nil
		case "enter":
			m.acceptSlashPopup()
			return m, nil
		case "tab":
			// Match Claude's behavior: Tab accepts the highlighted entry,
			// same as Enter. Avoids the awkward double-cycle (popup +
			// Tab-completion would race otherwise).
			m.acceptSlashPopup()
			return m, nil
		case "esc":
			m.slashPopupCmds = nil
			m.slashPopupCursor = 0
			return m, nil
		}
	}
	switch msg.String() {
	case "tab":
		// /command autocompletion takes precedence over @name — once the
		// user has typed a leading "/", they're committed to a slash
		// command and the @name catalog is irrelevant.
		curVal := m.compose.Value()
		if curVal != m.slashCompletionLast {
			// User has edited since our last slash-tab — restart the cycle.
			m.completionIndex = 0
		}
		if newVal, ok := trySlashComplete(curVal, m.completionIndex); ok {
			m.compose.SetValue(newVal)
			m.compose.CursorEnd()
			m.slashCompletionLast = newVal
			m.completionIndex++
			m.updateSlashPopup()
			return m, nil
		}
		if m.tryComplete() {
			return m, nil
		}
		// D8: bare Tab with no autocomplete to consume now toggles the
		// active pane. Power users who want the legacy "cycle focused
		// session directly" behavior have Ctrl+Tab / Shift+Tab.
		if m.activePane == PaneRail {
			m.activePane = PaneConversation
		} else {
			m.activePane = PaneRail
		}
		return m, nil
	case "ctrl+\\":
		// D8: legacy session-cycling power-user shortcut. Bubble Tea's
		// keyboard reporting on most terminals can't distinguish
		// Ctrl+Tab from plain Tab (both arrive as ASCII HT / 0x09), so
		// the dedicated forward-cycle chord moved to Ctrl+\ which IS
		// reliably reportable. Shift+Tab continues to cycle in reverse.
		m.cycleFocusedSession(+1)
		return m, nil
	case "shift+tab":
		m.cycleFocusedSession(-1)
		return m, nil
	case "up", "k":
		// Compose forwarding fallthrough is below; only intercept these
		// when the compose bar is empty so the user can still type 'k'.
		// D8: dispatch by active pane — rail moves the cursor, conversation
		// scrolls.
		if m.compose.Value() == "" {
			if m.activePane == PaneRail {
				m.moveRailCursor(-1)
			} else {
				m.scrollUp(1)
			}
			return m, nil
		}
	case "down", "j":
		if m.compose.Value() == "" {
			if m.activePane == PaneRail {
				m.moveRailCursor(+1)
			} else {
				m.scrollDown(1)
			}
			return m, nil
		}
	case "pgup", "ctrl+u":
		if m.compose.Value() == "" {
			if m.activePane == PaneRail {
				m.moveRailCursor(-5)
			} else {
				m.scrollUp(m.halfViewportPage())
			}
			return m, nil
		}
	case "pgdown", "ctrl+d":
		if m.compose.Value() == "" {
			if m.activePane == PaneRail {
				m.moveRailCursor(+5)
			} else {
				m.scrollDown(m.halfViewportPage())
			}
			return m, nil
		}
	case "home":
		if m.compose.Value() == "" {
			if m.activePane == PaneRail {
				m.railCursor = 0
				// Re-find a non-separator row at or after index 0.
				rows := m.railRows()
				for i, r := range rows {
					if r.Kind != RailRowUnfiledSeparator {
						m.railCursor = i
						break
					}
				}
				m.focusRailRow()
			} else {
				m.scrollToTop()
			}
			return m, nil
		}
	case "end":
		if m.compose.Value() == "" {
			if m.activePane == PaneRail {
				rows := m.railRows()
				if len(rows) > 0 {
					// Walk back from the last index to find a real row
					// (skip separators).
					for i := len(rows) - 1; i >= 0; i-- {
						if rows[i].Kind != RailRowUnfiledSeparator {
							m.railCursor = i
							break
						}
					}
					m.focusRailRow()
				}
			} else {
				m.scrollToBottom()
			}
			return m, nil
		}
	case "G":
		// Vim-style jump-to-end. Capital G is unambiguous (compose
		// would emit lowercase g for normal typing), so it doesn't
		// fight the textinput when compose is non-empty either, but
		// we keep the compose-empty gate for parity with the other
		// scroll keys.
		if m.compose.Value() == "" {
			if m.activePane == PaneRail {
				rows := m.railRows()
				if len(rows) > 0 {
					for i := len(rows) - 1; i >= 0; i-- {
						if rows[i].Kind != RailRowUnfiledSeparator {
							m.railCursor = i
							break
						}
					}
					m.focusRailRow()
				}
			} else {
				m.scrollToBottom()
			}
			return m, nil
		}
	case " ":
		// Space toggles a folder header's collapse state, but only when
		// compose is empty (otherwise space goes to the textinput).
		if m.compose.Value() == "" {
			rows := m.railRows()
			if m.railCursor >= 0 && m.railCursor < len(rows) {
				r := rows[m.railCursor]
				if r.Kind == RailRowFolder {
					m.groupCollapsed[r.GroupName] = !m.groupCollapsed[r.GroupName]
					_ = SaveTUIState(TUIState{
						GroupsCollapsed: collapsedGroupNames(m.groupCollapsed),
						RailCollapsed:   m.railCollapsed,
					})
					return m, nil
				}
			}
		}
	case "ctrl+a":
		m.attach = attachState{
			selected: map[int]bool{},
			loading:  true,
		}
		m.mode = ModeAttach
		return m, m.scanAttachCandidates()
	case "ctrl+b":
		m.mode = ModeBroadcast
		m.bcast = broadcastState{selected: map[string]bool{}}
		return m, nil
	case "ctrl+n":
		m.openSpawnModal()
		return m, nil
	case "ctrl+k":
		m.search.query.SetValue("")
		m.search.query.Focus()
		m.mode = ModeSearch
		return m, nil
	case "ctrl+r":
		return m.enterRenameMode()
	case "ctrl+f":
		// New folder modal (Phase A). The plan asked for Ctrl+M but
		// terminals emit CR for both Ctrl+M and Enter (bubbletea
		// resolves both to "enter"), so we use Ctrl+F instead — same
		// "f" mnemonic as "folder" and free in our keymap.
		return m.openNewFolderModal()
	case "ctrl+p":
		s := m.focusedSession()
		if s == nil || s.Status != "dead" {
			return m, nil
		}
		return m, m.doRespawn(s.Name, s.Cwd, s.Tags)
	case "?":
		// Only intercept '?' as the help key when compose is empty,
		// otherwise the user can't type a literal '?' mid-prompt.
		if m.compose.Value() == "" {
			m.mode = ModeHelp
			return m, nil
		}
	case "ctrl+h":
		m.mode = ModeHistory
		m.history = historyState{}
		return m, m.loadHubRuns()
	case "ctrl+j":
		m.railCollapsed = !m.railCollapsed
		_ = SaveTUIState(TUIState{
			GroupsCollapsed: collapsedGroupNames(m.groupCollapsed),
			RailCollapsed:   m.railCollapsed,
		})
		return m, nil
	case "/":
		// Bare "/" is now reserved for typing slash commands into the
		// compose bar (autocompleted via Tab). Grep transcripts moved to
		// Ctrl+G to keep the palette reachable without stealing the
		// slash key from the compose buffer.
	case "ctrl+g":
		if m.compose.Value() == "" {
			m.mode = ModeGrep
			m.grep.query = views.NewGrepQuery()
			m.grep.results = nil
			m.grep.cursor = 0
			return m, nil
		}
	case "ctrl+y":
		return m, m.copyConversation()
	case "ctrl+o":
		// Open the file viewer with a path prompt pre-filled with the
		// focused session's cwd (with trailing slash) so the user just
		// types a relative filename. If the focused session has no cwd,
		// we still open the prompt — the user can paste an absolute
		// path.
		return m.openEditorPathPrompt()
	case "ctrl+]":
		// Open the most-recent path detected in the conversation. If
		// there's nothing yet, fall back to the path prompt so the user
		// understands why nothing happened.
		if len(m.editor.recentPaths) == 0 {
			return m.openEditorPathPrompt()
		}
		path := m.editor.recentPaths[0]
		return m, m.loadEditorFile(path)
	case "ctrl+e":
		// Toggle editor pane visibility. If nothing's loaded yet,
		// route to the path prompt so the toggle is never a no-op.
		if m.editor.path == "" {
			return m.openEditorPathPrompt()
		}
		m.editor.visible = !m.editor.visible
		return m, nil
	case "enter":
		// D8: rail-pane Enter (with empty compose) focuses the cursored
		// session OR toggles a folder header's collapse. Conversation-
		// pane Enter (or any non-empty compose) falls through to send.
		if m.compose.Value() == "" && m.activePane == PaneRail {
			rows := m.railRows()
			if m.railCursor >= 0 && m.railCursor < len(rows) {
				r := rows[m.railCursor]
				switch r.Kind {
				case RailRowFolder:
					m.groupCollapsed[r.GroupName] = !m.groupCollapsed[r.GroupName]
					_ = SaveTUIState(TUIState{
						GroupsCollapsed: collapsedGroupNames(m.groupCollapsed),
						RailCollapsed:   m.railCollapsed,
					})
					return m, nil
				case RailRowSession:
					m.focused = r.SessionIdx
					return m, nil
				}
			}
			return m, nil
		}
		return m, m.sendComposed()
	case "shift+enter":
		cur := m.compose.Value()
		m.compose.SetValue(cur + "\n")
		return m, nil
	}
	// Default: forward to compose textinput.
	var cmd tea.Cmd
	m.compose, cmd = m.compose.Update(msg)
	// Recompute popup state every time the compose value may have
	// changed. Cheap (linear over a tiny catalog) and keeps the popup
	// in lock-step with what the user sees.
	m.updateSlashPopup()
	return m, cmd
}

func (m Model) handleKeyBroadcast(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		return m, nil
	case "tab":
		// In the textarea, Tab tries slash-command completion first;
		// only if that doesn't apply do we cycle fields. Other fields
		// use Tab for field-cycling unconditionally.
		if m.bcast.field == 1 {
			if m.bcast.text != m.bcast.compLast {
				m.bcast.compIndex = 0
			}
			if newVal, ok := trySlashComplete(m.bcast.text, m.bcast.compIndex); ok {
				m.bcast.text = newVal
				m.bcast.compLast = newVal
				m.bcast.compIndex++
				return m, nil
			}
		}
		m.bcast.field = (m.bcast.field + 1) % 3
		return m, nil
	case "shift+tab":
		m.bcast.field = (m.bcast.field + 2) % 3
		return m, nil
	}
	switch m.bcast.field {
	case 0: // session list
		switch msg.String() {
		case "up", "k":
			if m.bcast.cursor > 0 {
				m.bcast.cursor--
			}
		case "down", "j":
			if m.bcast.cursor < len(m.sessions)-1 {
				m.bcast.cursor++
			}
		case " ", "x":
			if m.bcast.cursor >= 0 && m.bcast.cursor < len(m.sessions) {
				sid := m.sessions[m.bcast.cursor].ID
				m.bcast.selected[sid] = !m.bcast.selected[sid]
			}
		case "a":
			// Select all live, non-readonly sessions (the only ones broadcast can target).
			for _, s := range m.sessions {
				if s.Status != "dead" && s.Kind != "readonly" {
					m.bcast.selected[s.ID] = true
				}
			}
		case "n":
			// Deselect everything.
			m.bcast.selected = map[string]bool{}
		case "i":
			// Invert selection (over the broadcast-eligible set).
			for _, s := range m.sessions {
				if s.Status == "dead" || s.Kind == "readonly" {
					continue
				}
				m.bcast.selected[s.ID] = !m.bcast.selected[s.ID]
			}
		}
	case 1: // textarea
		switch msg.String() {
		case "enter":
			m.bcast.text += "\n"
			m.bcast.compIndex = 0
		case "backspace":
			if len(m.bcast.text) > 0 {
				m.bcast.text = m.bcast.text[:len(m.bcast.text)-1]
			}
			m.bcast.compIndex = 0
		default:
			if msg.Type == tea.KeyRunes {
				m.bcast.text += string(msg.Runes)
				m.bcast.compIndex = 0
			} else if msg.String() == " " {
				m.bcast.text += " "
				m.bcast.compIndex = 0
			}
		}
	case 2: // send button
		if msg.String() == "enter" {
			return m, m.sendBroadcast()
		}
	}
	return m, nil
}

func (m Model) handleKeyGrep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		return m, nil
	case "up":
		if m.grep.cursor > 0 {
			m.grep.cursor--
		}
		return m, nil
	case "down":
		if m.grep.cursor < len(m.grep.results)-1 {
			m.grep.cursor++
		}
		return m, nil
	case "enter":
		// Jump to the result's session: focus it on the main view.
		if m.grep.cursor >= 0 && m.grep.cursor < len(m.grep.results) {
			r := m.grep.results[m.grep.cursor]
			if sid, _ := r["session_id"].(string); sid != "" {
				for i, s := range m.sessions {
					if s.ID == sid {
						m.focused = i
						break
					}
				}
			}
		}
		m.mode = ModeMain
		return m, nil
	}
	// Forward to query textinput, then schedule a debounced search.
	var cmd tea.Cmd
	m.grep.query, cmd = m.grep.query.Update(msg)
	m.grep.debounceToken++
	tok := m.grep.debounceToken
	debounce := tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg {
		return grepDebounceMsg{token: tok}
	})
	return m, tea.Batch(cmd, debounce)
}

func (m Model) handleKeySearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.search.query.SetValue("")
		m.mode = ModeMain
		return m, nil
	case "enter":
		// Keep the filter; return to ModeMain so user can navigate the
		// filtered rail. Esc later (in ModeMain) doesn't clear it —
		// only re-entering search and pressing Esc does.
		m.mode = ModeMain
		return m, nil
	case "up", "k":
		m.moveRailCursor(-1)
		return m, nil
	case "down", "j":
		m.moveRailCursor(+1)
		return m, nil
	}
	var cmd tea.Cmd
	m.search.query, cmd = m.search.query.Update(msg)
	// Re-snap cursor onto a visible session if the focused one fell out
	// of the filter.
	if m.search.query.Value() != "" {
		rows := m.railSessionRows()
		if len(rows) > 0 {
			found := false
			for _, r := range rows {
				if r.SessionIdx == m.focused {
					found = true
					break
				}
			}
			if !found {
				m.focused = rows[0].SessionIdx
			}
			m.syncRailCursorToFocus()
		}
	}
	return m, cmd
}

func (m Model) handleKeySpawn(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		return m, nil
	case "tab":
		// In the cwd field, Tab tries directory-name completion first.
		// Only if there's nothing to complete do we fall through to the
		// existing Tab=cycle-field behavior — this matches the bash/zsh
		// muscle memory users already have for path completion.
		if m.spawn.field == 1 {
			cur := m.spawn.cwd.Value()
			if cur != m.spawn.pathCompletionLast {
				m.spawn.pathCompletionIndex = 0
			}
			if newVal, ok, total := tryPathComplete(cur, m.spawn.pathCompletionIndex); ok {
				m.spawn.cwd.SetValue(newVal)
				m.spawn.cwd.SetCursor(len(newVal))
				m.spawn.pathCompletionLast = newVal
				if total > 1 {
					m.spawn.pathCompletionIndex++
				}
				return m, nil
			}
		}
		m.spawn.field = (m.spawn.field + 1) % 3
		m.refocusSpawn()
		return m, nil
	case "shift+tab":
		m.spawn.field = (m.spawn.field + 2) % 3
		m.refocusSpawn()
		return m, nil
	case "ctrl+p":
		// Recent cwds picker — cwd field only. First press fetches via
		// RPC; subsequent presses cycle through the cached list.
		if m.spawn.field == 1 {
			return m, m.spawnCwdRecentNext()
		}
	case "enter":
		name := strings.TrimSpace(m.spawn.name.Value())
		if name == "" {
			m.spawn.field = 0
			m.refocusSpawn()
			return m, nil
		}
		cwd := views.ExpandHome(strings.TrimSpace(m.spawn.cwd.Value()))
		folder := strings.TrimSpace(m.spawn.folder.Value())
		// As of D10b the folder field is purely TUI-side: tags is empty,
		// and the spawn handler assigns into the folder via folders.json
		// after the daemon confirms the new session.
		return m, m.doSpawn(name, cwd, nil, folder)
	}
	var cmd tea.Cmd
	switch m.spawn.field {
	case 0:
		m.spawn.name, cmd = m.spawn.name.Update(msg)
	case 1:
		m.spawn.cwd, cmd = m.spawn.cwd.Update(msg)
	case 2:
		m.spawn.folder, cmd = m.spawn.folder.Update(msg)
	}
	return m, cmd
}

// openSpawnModal seeds spawnState (cwd defaults to focused session's
// cwd or $HOME, folder defaults to the focused session's currently-
// assigned folder when there is one) and switches to ModeSpawn.
// Pointer receiver because we mutate m.mode and m.spawn. Called by
// Ctrl+N and by the auto-open path in listMsg when the first list
// comes back empty.
func (m *Model) openSpawnModal() {
	cwd := ""
	folder := ""
	if s := m.focusedSession(); s != nil {
		cwd = s.Cwd
		// Pre-fill folder with the focused session's current folder
		// (TUI-side). If it's not in any folder, leave the field empty
		// — the new session lands in the unfiled list.
		folder = m.folders.FolderForSession(s.ID)
	}
	if cwd == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cwd = home
		}
	}
	m.spawn = spawnState{
		name:   views.NewSpawnNameInput(),
		cwd:    views.NewSpawnCwdInput(cwd),
		folder: views.NewSpawnFolderInput(folder),
		field:  0,
	}
	m.mode = ModeSpawn
}

// refocusSpawn applies Focus()/Blur() so only the active spawn-modal
// field shows the cursor. Called whenever m.spawn.field changes.
func (m *Model) refocusSpawn() {
	m.spawn.name.Blur()
	m.spawn.cwd.Blur()
	m.spawn.folder.Blur()
	switch m.spawn.field {
	case 0:
		m.spawn.name.Focus()
	case 1:
		m.spawn.cwd.Focus()
	case 2:
		m.spawn.folder.Focus()
	}
}

// autoSpawnDefault spawns a default "temp" session at $HOME so a freshly
// booted TUI with no existing sessions has something to chat with
// immediately. If the spawn fails (likely a name collision with a
// leftover "temp" from a previous run) we cycle through "temp-2",
// "temp-3", ... up to a small cap and only fall back to the modal when
// every variant is taken or the failure isn't a name collision.
func (m Model) autoSpawnDefault() tea.Cmd {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Can't auto-default — punt to the modal so the user can decide.
		return func() tea.Msg { return autoSpawnFallbackMsg{} }
	}
	c := m.client
	return func() tea.Msg {
		// Try names temp, temp-2, temp-3, ... up to a reasonable cap.
		for n := 1; n <= 9; n++ {
			name := "temp"
			if n > 1 {
				name = fmt.Sprintf("temp-%d", n)
			}
			_, err := c.Call(context.Background(), "spawn_session",
				map[string]any{"name": name, "cwd": home, "tags": []string{}})
			if err != nil {
				// ChubError with NAME_TAKEN? Try the next variant.
				le := strings.ToLower(err.Error())
				if strings.Contains(le, "name") && strings.Contains(le, "in use") {
					continue
				}
				// Other error — fall back to the modal.
				return autoSpawnFallbackMsg{err: err}
			}
			return autoSpawnedMsg{name: name, cwd: home}
		}
		return autoSpawnFallbackMsg{}
	}
}

// prettyHomePath collapses a $HOME prefix to "~" for the toast — the
// user reads "~/projects/foo", not "/Users/very/long/projects/foo".
func prettyHomePath(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// spawnCwdRecentNext powers Ctrl+P in the spawn modal's cwd field.
//
// First press: not-yet-loaded → fire the recent_cwds RPC and on response
// the Update handler caches the list, sets the field to the first
// entry, and seeds recentCwdIndex.
//
// Subsequent presses: list is cached locally, so we synthesize the
// "next index" advance as a tea.Msg rather than mutating m here — the
// reducer is the canonical place to update m.spawn.cwd so the cursor
// state stays consistent. We don't bother with an RPC re-fetch on
// repeat presses; the cached list is fresh enough for one modal session.
func (m Model) spawnCwdRecentNext() tea.Cmd {
	if m.spawn.recentCwdsLoaded {
		// Synthetic "advance" path: emit a loaded msg with the cached
		// slice. The Update handler bumps the index; if the list is
		// empty we surface a soft err there.
		cached := m.spawn.recentCwds
		return func() tea.Msg {
			return spawnRecentCwdsLoadedMsg{cwds: cached}
		}
	}
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "recent_cwds",
			map[string]any{"limit": 20})
		if err != nil {
			return spawnRecentCwdsLoadedMsg{err: err}
		}
		var r struct {
			Cwds []string `json:"cwds"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return spawnRecentCwdsLoadedMsg{err: err}
		}
		return spawnRecentCwdsLoadedMsg{cwds: r.Cwds}
	}
}

// doSpawn fires the spawn_session RPC. tags is forwarded verbatim to
// the daemon (kept as a list of strings for forward-compat — the
// pre-D10b spawn modal turned the "group" field into the first tag,
// but the modal no longer does this; callers pass an empty slice).
//
// folder, when non-empty, assigns the freshly-spawned session into a
// TUI-side folder (folders.json). This is intentionally part of the
// spawn flow rather than a separate Cmd because the assignment must
// see the new session id, which only the spawn RPC's reply carries —
// piping it back through a bare spawnDoneMsg would force every caller
// to know about folders.
func (m Model) doSpawn(name, cwd string, tags []string, folder string) tea.Cmd {
	c := m.client
	if tags == nil {
		tags = []string{}
	}
	return func() tea.Msg {
		params := map[string]any{
			"name": name,
			"cwd":  cwd,
			"tags": tags,
		}
		raw, err := c.Call(context.Background(), "spawn_session", params)
		if err != nil {
			return spawnFailedMsg{err}
		}
		if folder == "" {
			return spawnDoneMsg{}
		}
		// Best-effort folder assignment. We don't fail the spawn on an
		// assignment error — the session is already created and the
		// user can /movetofolder it later.
		var r struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
		}
		if err := json.Unmarshal(raw, &r); err == nil && r.Session.ID != "" {
			st := LoadFolders()
			st.Assign(folder, r.Session.ID)
			_ = SaveFolders(st)
		}
		return spawnDoneMsg{}
	}
}

// doRespawn resurrects a dead session: spawns a new wrapper with the
// same cwd and tags under a temp name (the dead row still occupies the
// original name in the registry until it actually transitions), then
// renames the new session back to the original. The dead row stays in
// the DB but doesn't conflict — the registry's name-uniqueness check
// excludes DEAD status (see registry.register / registry.rename).
func (m Model) doRespawn(name, cwd string, tags []string) tea.Cmd {
	c := m.client
	if tags == nil {
		tags = []string{}
	}
	return func() tea.Msg {
		tempName := name + "-r"
		out, err := c.Call(context.Background(), "spawn_session", map[string]any{
			"name": tempName,
			"cwd":  cwd,
			"tags": tags,
		})
		if err != nil {
			return errMsg{err}
		}
		var r struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
		}
		if err := json.Unmarshal(out, &r); err != nil {
			return errMsg{err}
		}
		if _, err := c.Call(context.Background(), "rename_session", map[string]any{
			"id":   r.Session.ID,
			"name": name,
		}); err != nil {
			return errMsg{err}
		}
		return respawnDoneMsg{}
	}
}

// enterRenameMode inspects the rail row under the cursor and opens
// ModeRename with the appropriate target. Does nothing (and stays in
// ModeMain) if the rail is empty or the cursor is out of range.
func (m Model) enterRenameMode() (tea.Model, tea.Cmd) {
	rows := m.railRows()
	if m.railCursor < 0 || m.railCursor >= len(rows) {
		return m, nil
	}
	row := rows[m.railCursor]
	input := textinput.New()
	input.Prompt = "▸ "
	input.CharLimit = 0
	switch row.Kind {
	case RailRowSession:
		s := row.Session
		input.SetValue(s.Name)
		input.CursorEnd()
		input.Focus()
		m.rename = renameState{
			input:    input,
			target:   RenameSession,
			sessions: []string{s.ID},
			oldName:  s.Name,
		}
	case RailRowFolder:
		// User folder rename — does not retag any sessions; just
		// rewrites the folder key in folders.json. sessions slice is
		// empty because no per-session RPC is required.
		input.SetValue(row.GroupName)
		input.CursorEnd()
		input.Focus()
		m.rename = renameState{
			input:    input,
			target:   RenameFolder,
			sessions: nil,
			oldName:  row.GroupName,
		}
	default:
		return m, nil
	}
	m.mode = ModeRename
	return m, nil
}

func (m Model) handleKeyRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		return m, nil
	case "enter":
		newName := strings.TrimSpace(m.rename.input.Value())
		if newName == "" || newName == m.rename.oldName {
			m.mode = ModeMain
			return m, nil
		}
		switch m.rename.target {
		case RenameSession:
			return m, m.doRenameSession(m.rename.sessions[0], newName)
		case RenameFolder:
			return m, m.doRenameFolder(m.rename.oldName, newName)
		}
		// RenameGroup: bulk retag all sessions in the group.
		return m, m.doRenameGroup(m.rename.sessions, m.rename.oldName, newName)
	}
	var cmd tea.Cmd
	m.rename.input, cmd = m.rename.input.Update(msg)
	return m, cmd
}

// openNewFolderModal switches to ModeNewFolder with a fresh, focused
// textinput. Pointer-flavored body via assignment to m.mode + m.newFolder
// inside this value receiver — Update() picks up the returned tea.Model.
func (m Model) openNewFolderModal() (tea.Model, tea.Cmd) {
	input := textinput.New()
	input.Prompt = "▸ "
	input.CharLimit = 0
	input.Focus()
	m.newFolder = newFolderState{input: input}
	m.mode = ModeNewFolder
	return m, nil
}

// handleKeyNewFolder is the reducer for ModeNewFolder. Enter creates
// the folder (rejecting duplicates inline via newFolder.err); Esc
// cancels.
func (m Model) handleKeyNewFolder(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		m.newFolder = newFolderState{}
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.newFolder.input.Value())
		if name == "" {
			m.newFolder.err = fmt.Errorf("name required")
			return m, nil
		}
		// Reload from disk in case another concurrent TUI session
		// added a folder; the in-memory state is the source of truth
		// for rendering but disk is the source of truth for
		// uniqueness.
		st := LoadFolders()
		if err := st.CreateFolder(name); err != nil {
			m.newFolder.err = err
			return m, nil
		}
		if err := SaveFolders(st); err != nil {
			m.newFolder.err = err
			return m, nil
		}
		m.folders = st
		m.mode = ModeMain
		m.newFolder = newFolderState{}
		return m, nil
	}
	var cmd tea.Cmd
	m.newFolder.input, cmd = m.newFolder.input.Update(msg)
	// Clear the error on any subsequent edit so the user isn't stuck
	// staring at a stale "already exists" line after backspacing.
	if m.newFolder.err != nil {
		m.newFolder.err = nil
	}
	return m, cmd
}

// viewNewFolder renders the centered new-folder modal.
func (m Model) viewNewFolder() string {
	w := m.width
	if w < 50 {
		w = 50
	}
	cw := w - 12
	if cw < 30 {
		cw = 30
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("New folder") + "\n\n")
	b.WriteString("  name: " + m.newFolder.input.View() + "\n\n")
	if m.newFolder.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Render("error: "+m.newFolder.err.Error()) + "\n\n")
	}
	b.WriteString(dim.Render("Enter to create · Esc cancel"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(cw).
		Padding(0, 1).
		Render(b.String())
	wh, hh := m.width, m.height
	if wh < 1 {
		wh = w
	}
	if hh < 1 {
		hh = 10
	}
	return lipgloss.Place(wh, hh, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) doRenameSession(sid, newName string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		_, err := c.Call(context.Background(), "rename_session",
			map[string]any{"id": sid, "name": newName})
		if err != nil {
			return errMsg{err}
		}
		return renameDoneMsg{}
	}
}

func (m Model) doRenameGroup(sids []string, oldGroup, newGroup string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		// For each session: add the new tag; remove the old tag. The
		// daemon's set_session_tags RPC handles both adds and removes
		// in one call.
		for _, sid := range sids {
			_, err := c.Call(context.Background(), "set_session_tags",
				map[string]any{
					"id":     sid,
					"add":    []string{newGroup},
					"remove": []string{oldGroup},
				})
			if err != nil {
				return errMsg{err}
			}
		}
		return renameDoneMsg{}
	}
}

// doRenameFolder rewrites a user folder name in folders.json. Unlike
// doRenameGroup it doesn't issue any RPCs — folders are TUI-local. On
// success it returns renameDoneMsg so the standard rename reducer
// path runs (mode -> Main, refresh sessions); on collision/error it
// returns spawnFailedMsg-shape error embedded in renameState.err via
// a renameFolderFailedMsg so the modal stays open.
type renameFolderFailedMsg struct{ err error }

func (m Model) doRenameFolder(oldName, newName string) tea.Cmd {
	return func() tea.Msg {
		st := LoadFolders()
		if err := st.RenameFolder(oldName, newName); err != nil {
			return renameFolderFailedMsg{err}
		}
		if err := SaveFolders(st); err != nil {
			return renameFolderFailedMsg{err}
		}
		return renameDoneMsg{}
	}
}

// handleKeyAttach is the reducer for ModeAttach: navigate the candidate
// list, toggle selections, and either attach (Enter), bulk-detach (d),
// or cancel (Esc). 'a' selects every candidate that isn't already
// attached; 'n' clears; 'r' rescans.
func (m Model) handleKeyAttach(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle the 'd' bulk-detach confirm prompt first — when active,
	// only y/n/esc are meaningful. Everything else is a no-op so a stray
	// keystroke can't accidentally proceed.
	if m.attach.confirmDetachN > 0 {
		switch msg.String() {
		case "y", "Y":
			n := m.attach.confirmDetachN
			m.attach.confirmDetachN = 0
			return m, m.doDetachSelected(n)
		case "n", "N", "esc":
			m.attach.confirmDetachN = 0
			m.attach.notice = "detach cancelled"
			return m, nil
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		return m, nil
	case "up", "k":
		if m.attach.cursor > 0 {
			m.attach.cursor--
		}
		return m, nil
	case "down", "j":
		if m.attach.cursor < len(m.attach.candidates)-1 {
			m.attach.cursor++
		}
		return m, nil
	case " ", "x":
		if m.attach.cursor >= 0 && m.attach.cursor < len(m.attach.candidates) {
			m.attach.selected[m.attach.cursor] = !m.attach.selected[m.attach.cursor]
		}
		return m, nil
	case "a":
		// Select every candidate that isn't already attached. Already-
		// attached rows would error or no-op on the daemon side; we just
		// skip them at the source.
		for i, c := range m.attach.candidates {
			if attached, _ := c["already_attached"].(bool); !attached {
				m.attach.selected[i] = true
			}
		}
		return m, nil
	case "n":
		m.attach.selected = map[int]bool{}
		return m, nil
	case "r":
		m.attach.loading = true
		m.attach.candidates = nil
		m.attach.selected = map[int]bool{}
		return m, m.scanAttachCandidates()
	case "d":
		// Bulk-detach the selected items that are CURRENTLY chubby-
		// managed. Skip non-attached selections silently; they're not
		// detachable. Show a confirm prompt with the actionable count.
		n := 0
		for i, c := range m.attach.candidates {
			if !m.attach.selected[i] {
				continue
			}
			if attached, _ := c["already_attached"].(bool); attached {
				n++
			}
		}
		if n == 0 {
			m.attach.notice = "nothing to detach (none of the selected are chubby-managed)"
			return m, nil
		}
		m.attach.confirmDetachN = n
		return m, nil
	case "enter":
		if len(m.attach.selected) == 0 {
			m.attach.notice = "select at least one with Space"
			return m, nil
		}
		// Trim away selections that point past the candidates slice (a
		// concurrent rescan could shrink it). Defensive — Enter on stale
		// state shouldn't blow up.
		any := false
		for i := range m.attach.selected {
			if m.attach.selected[i] && i >= 0 && i < len(m.attach.candidates) {
				any = true
				break
			}
		}
		if !any {
			m.attach.notice = "select at least one with Space"
			return m, nil
		}
		return m, m.doAttachSelected()
	}
	return m, nil
}

// doDetachSelected fires release_session for every selected candidate
// that's currently chubby-managed (already_attached=true). The
// candidate's session id isn't in scan_candidates output, so we resolve
// by pid via a list_sessions lookup. The 'expectedN' is the count we
// promised the user in the confirm prompt — used for the result toast.
func (m Model) doDetachSelected(expectedN int) tea.Cmd {
	type pick struct {
		pid int
	}
	var picks []pick
	for i, c := range m.attach.candidates {
		if !m.attach.selected[i] {
			continue
		}
		attached, _ := c["already_attached"].(bool)
		if !attached {
			continue
		}
		pidF, _ := c["pid"].(float64)
		picks = append(picks, pick{pid: int(pidF)})
	}
	c := m.client
	return func() tea.Msg {
		// Resolve session ids by pid via list_sessions.
		raw, err := c.Call(context.Background(), "list_sessions", map[string]any{})
		if err != nil {
			return attachDoneMsg{err: err}
		}
		var r struct {
			Sessions []Session `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return attachDoneMsg{err: err}
		}
		// Map pid → session id. Sessions don't include pid in the JSON we
		// expose via SessionDict today (the field exists on the daemon
		// side but isn't surfaced to clients); fall back to scanning by
		// cwd if needed. For now: use cwd as the join key — scan
		// candidates carry cwd, and list_sessions sessions carry cwd.
		_ = picks
		// Simpler join: candidate cwd → session.id where session.cwd matches.
		byCwd := map[string]string{}
		for _, s := range r.Sessions {
			if s.Status != "dead" {
				byCwd[s.Cwd] = s.ID
			}
		}
		released := 0
		var lastErr error
		for i, cand := range m.attach.candidates {
			if !m.attach.selected[i] {
				continue
			}
			attached, _ := cand["already_attached"].(bool)
			if !attached {
				continue
			}
			cwd, _ := cand["cwd"].(string)
			sid := byCwd[cwd]
			if sid == "" {
				continue
			}
			if _, err := c.Call(context.Background(), "release_session",
				map[string]any{"id": sid}); err != nil {
				lastErr = err
				continue
			}
			released++
		}
		if released == 0 && lastErr != nil {
			return attachDoneMsg{err: lastErr}
		}
		return attachDoneMsg{n: released, detached: true}
	}
}

// scanAttachCandidates fires the daemon's scan_candidates RPC and
// returns the result as an attachScannedMsg. The candidates payload is
// kept as []map[string]any so unknown fields propagate to the renderer
// untouched.
func (m Model) scanAttachCandidates() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "scan_candidates", map[string]any{})
		if err != nil {
			return attachScannedMsg{err: err}
		}
		var r struct {
			Candidates []map[string]any `json:"candidates"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return attachScannedMsg{err: err}
		}
		return attachScannedMsg{candidates: r.Candidates}
	}
}

// doAttachSelected snapshots the currently-selected candidates and
// issues one attach_tmux / attach_existing_readonly RPC per pick. Each
// session is auto-named "<basename(cwd)>-<pid>" so the rail row gets a
// recognisable label without prompting; the user can /rename later.
//
// On the first error we stop and report what we got, mirroring the rest
// of the codebase's "fail loud, fail early" RPC pattern.
func (m Model) doAttachSelected() tea.Cmd {
	var picks []map[string]any
	for i, c := range m.attach.candidates {
		if m.attach.selected[i] && i >= 0 && i < len(m.attach.candidates) {
			picks = append(picks, c)
		}
	}
	c := m.client
	return func() tea.Msg {
		attached := 0
		for _, cand := range picks {
			classification, _ := cand["classification"].(string)
			cwd, _ := cand["cwd"].(string)
			pidF, _ := cand["pid"].(float64)
			pid := int(pidF)
			tmuxTarget, _ := cand["tmux_target"].(string)
			// Auto-name: <basename(cwd)>-<pid>. Fallback "session" when cwd
			// is empty or pathologically short.
			base := filepath.Base(cwd)
			if base == "" || base == "/" || base == "." {
				base = "session"
			}
			name := fmt.Sprintf("%s-%d", base, pid)

			switch classification {
			case "tmux_full":
				if _, err := c.Call(context.Background(), "attach_tmux", map[string]any{
					"name":        name,
					"cwd":         cwd,
					"pid":         pid,
					"tmux_target": tmuxTarget,
					"tags":        []string{},
				}); err != nil {
					return attachDoneMsg{n: attached, err: err}
				}
			case "promote_required":
				if _, err := c.Call(context.Background(), "attach_existing_readonly", map[string]any{
					"pid":  pid,
					"cwd":  cwd,
					"name": name,
				}); err != nil {
					return attachDoneMsg{n: attached, err: err}
				}
			}
			attached++
		}
		return attachDoneMsg{n: attached}
	}
}

func (m Model) handleKeyHistory(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		return m, nil
	case "tab":
		m.history.column = (m.history.column + 1) % 3
		return m, nil
	case "shift+tab":
		m.history.column = (m.history.column + 2) % 3
		return m, nil
	}
	switch m.history.column {
	case 0:
		switch msg.String() {
		case "up", "k":
			if m.history.runCursor > 0 {
				m.history.runCursor--
			}
		case "down", "j":
			if m.history.runCursor < len(m.history.runs)-1 {
				m.history.runCursor++
			}
		case "enter":
			if m.history.runCursor < len(m.history.runs) {
				return m, m.loadHubRun(m.history.runs[m.history.runCursor])
			}
		}
	case 1:
		switch msg.String() {
		case "up", "k":
			if m.history.sessCursor > 0 {
				m.history.sessCursor--
			}
		case "down", "j":
			if m.history.sessCursor < len(m.history.runSessions)-1 {
				m.history.sessCursor++
			}
		case "enter":
			if m.history.sessCursor < len(m.history.runSessions) &&
				m.history.runCursor < len(m.history.runs) {
				runID, _ := m.history.runs[m.history.runCursor]["id"].(string)
				name := m.history.runSessions[m.history.sessCursor].Name
				return m, m.loadLogPreview(runID, name)
			}
		}
	}
	return m, nil
}

func (m Model) loadHubRuns() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "list_hub_runs", nil)
		if err != nil {
			return errMsg{err}
		}
		var r struct {
			Runs []map[string]any `json:"runs"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return errMsg{err}
		}
		return historyRunsMsg(r.Runs)
	}
}

func (m Model) loadHubRun(run map[string]any) tea.Cmd {
	id, _ := run["id"].(string)
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "get_hub_run", map[string]any{"id": id})
		if err != nil {
			return errMsg{err}
		}
		var r struct {
			Run      map[string]any `json:"run"`
			Sessions []Session      `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return errMsg{err}
		}
		return historyRunSessionsMsg{sessions: r.Sessions, preview: ""}
	}
}

// loadLogPreview reads ${CHUBBY_HOME}/runs/<runID>/logs/<name>.log directly.
// Reading off disk is fine because the TUI runs on the same host as chubbyd.
func (m Model) loadLogPreview(runID, sessionName string) tea.Cmd {
	sessions := m.history.runSessions
	return func() tea.Msg {
		preview := views.ReadLogTail(runID, sessionName)
		return historyRunSessionsMsg{sessions: sessions, preview: preview}
	}
}

// attemptReconnect fires Reconnect on the client with a small backoff,
// re-subscribes events, and re-enters ModeMain on success. On failure
// it re-arms itself via tea.Tick.
func (m Model) attemptReconnect() (tea.Model, tea.Cmd) {
	m.mode = ModeReconnecting
	m.reconnectAttempts++
	c := m.client
	delay := time.Second
	switch {
	case m.reconnectAttempts > 8:
		delay = 8 * time.Second
	case m.reconnectAttempts > 4:
		delay = 4 * time.Second
	case m.reconnectAttempts > 2:
		delay = 2 * time.Second
	}
	return m, tea.Tick(delay, func(time.Time) tea.Msg {
		if err := c.Reconnect(); err != nil {
			return reconnectAttemptMsg{}
		}
		if _, err := c.Call(context.Background(), "subscribe_events", nil); err != nil {
			return reconnectAttemptMsg{}
		}
		return reconnectedMsg{}
	})
}

// doGrep is the actual FTS RPC. token is checked in the reducer to
// invalidate stale results when the user keeps typing.
func (m Model) doGrep(query string, token int) tea.Cmd {
	if query == "" {
		return func() tea.Msg { return grepResultsMsg{token: token, matches: nil} }
	}
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "search_transcripts", map[string]any{
			"query": query,
			"limit": 50,
		})
		if err != nil {
			return errMsg{err}
		}
		var r struct {
			Matches []map[string]any `json:"matches"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return errMsg{err}
		}
		return grepResultsMsg{token: token, matches: r.Matches}
	}
}

// sendBroadcast injects the textarea contents into every selected session.
func (m Model) sendBroadcast() tea.Cmd {
	text := m.bcast.text
	targets := make([]string, 0)
	for sid, sel := range m.bcast.selected {
		if sel {
			targets = append(targets, sid)
		}
	}
	c := m.client
	return func() tea.Msg {
		b64 := base64.StdEncoding.EncodeToString([]byte(text))
		for _, sid := range targets {
			_, _ = c.Call(context.Background(), "inject", map[string]any{
				"session_id":  sid,
				"payload_b64": b64,
			})
		}
		return bcastDoneMsg{n: len(targets)}
	}
}

// sendComposed parses an optional @name retarget prefix, resolves the
// target session id via list_sessions, then issues the inject RPC.
//
// chubby-side slash commands (/color, /rename, /tag) are intercepted here
// before any inject path: they modify the chubby session itself rather
// than the underlying Claude conversation, so they must never reach
// Claude.
func (m Model) sendComposed() tea.Cmd {
	text := m.compose.Value()
	if text == "" {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	// Chub-side commands intercepted before any inject path — match the
	// command head and pass everything else as the (possibly empty) arg
	// so the doChub* helpers can produce a usage error. Without this we
	// would burn a list_sessions/inject round-trip on a typo.
	if cmd, arg, ok := splitChubCommand(trimmed); ok {
		switch cmd {
		case "/color":
			return m.doChubColor(arg)
		case "/rename":
			return m.doChubRename(arg)
		case "/tag":
			return m.doChubTag(arg)
		case "/refresh-claude":
			_ = arg // /refresh-claude takes no args; ignore any tail.
			return m.doChubRefreshClaude()
		case "/movetofolder":
			return m.doChubMoveToFolder(arg)
		case "/removefromfolder":
			_ = arg // takes no args; ignore any tail.
			return m.doChubRemoveFromFolder()
		case "/detach":
			_ = arg // /detach takes no args; ignore any tail.
			return m.doChubDetach()
		}
	}
	target := ""
	if strings.HasPrefix(text, "@") {
		sp := strings.IndexByte(text, ' ')
		if sp > 1 {
			target = text[1:sp]
			text = text[sp+1:]
		}
	}
	if target == "" {
		if s := m.focusedSession(); s != nil {
			target = s.Name
		}
	}
	if target == "" {
		// No focused session and no @name retarget — guide the user
		// instead of firing a doomed list_sessions+inject round-trip.
		return func() tea.Msg {
			return composeFailedMsg{fmt.Errorf(
				"no session focused — press Ctrl+N to create one (or Tab once a session exists)",
			)}
		}
	}
	payload := text
	c := m.client
	return func() tea.Msg {
		raw, err := c.Call(context.Background(), "list_sessions", nil)
		if err != nil {
			return composeFailedMsg{err}
		}
		var r struct {
			Sessions []Session `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return composeFailedMsg{err}
		}
		var sid string
		for _, s := range r.Sessions {
			if s.Name == target {
				sid = s.ID
				break
			}
		}
		if sid == "" {
			return composeFailedMsg{fmt.Errorf("no session named %q", target)}
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(payload))
		if _, err := c.Call(context.Background(), "inject", map[string]any{
			"session_id":  sid,
			"payload_b64": b64,
		}); err != nil {
			return composeFailedMsg{err}
		}
		return composeSentMsg{}
	}
}

// splitChubCommand recognises a chubby-side slash command head ("/color",
// "/rename", "/tag", "/refresh-claude") at the start of the trimmed
// compose text and returns (head, remainder-trimmed, true). The
// remainder may be empty — the caller decides how to surface a usage
// error. Returns false for anything else, leaving the regular inject
// path to handle it.
//
// Heads are checked longest-first so "/refresh-claude" wins over a
// hypothetical "/refresh"; today there's no overlap, but the ordering
// is the right invariant.
func splitChubCommand(s string) (cmd, arg string, ok bool) {
	// Longest-first so "/removefromfolder" wins over a future
	// "/remove*" and "/movetofolder" wins over a future "/move*".
	for _, head := range []string{
		"/removefromfolder",
		"/movetofolder",
		"/refresh-claude",
		"/detach",
		"/color",
		"/rename",
		"/tag",
	} {
		if s == head {
			return head, "", true
		}
		if strings.HasPrefix(s, head+" ") {
			return head, strings.TrimSpace(s[len(head)+1:]), true
		}
	}
	return "", "", false
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

// pathRE matches an absolute file path with an extension, optionally
// followed by ":<line>". Anchored to be robust against trailing
// punctuation in the conversation. Restricted to absolute paths so we
// don't match arbitrary slash-containing words ("model/utils") that
// aren't on the user's filesystem.
var pathRE = regexp.MustCompile(`(/[\w\-./]+\.[a-zA-Z0-9]{1,8})(:\d+)?`)

// pushRecentPath inserts path at the head of the recent-paths slice,
// deduping (existing copies are removed) and capping at
// editorRecentPathsCap. Returns the new slice.
func pushRecentPath(slice []string, path string) []string {
	if path == "" {
		return slice
	}
	out := make([]string, 0, len(slice)+1)
	out = append(out, path)
	for _, p := range slice {
		if p == path {
			continue
		}
		out = append(out, p)
		if len(out) >= editorRecentPathsCap {
			break
		}
	}
	return out
}

// harvestPathsFromText scans a single turn's text for path mentions
// and pushes each match onto the recentPaths slice. Newer paths land
// at the head — the most-recently-spoken file is the one Ctrl+]
// opens.
func (m *Model) harvestPathsFromText(text string) {
	matches := pathRE.FindAllStringSubmatch(text, -1)
	for _, mm := range matches {
		path := mm[1]
		m.editor.recentPaths = pushRecentPath(m.editor.recentPaths, path)
	}
}

// extractPathsFromTurns walks the conversation and returns every
// absolute path mention, in order of appearance (most-recent turn
// first because callers want the head to be "most recent"). Strips
// trailing ":<line>" suffixes so the slice contains plain paths
// suitable for opening — the line number is re-extracted at open time
// via splitPathLine.
func extractPathsFromTurns(turns []Turn) []string {
	var out []string
	// Walk newest-first so the "most recent" path ends up at index 0
	// after dedupe in pushRecentPath.
	for i := len(turns) - 1; i >= 0; i-- {
		matches := pathRE.FindAllStringSubmatch(turns[i].Text, -1)
		for _, m := range matches {
			path := m[1]
			out = append(out, path)
		}
	}
	return out
}

// splitPathLine separates a "/foo/bar.py:42" string into ("/foo/bar.py",
// 42). When the input has no ":<line>" suffix it returns (input, 0).
func splitPathLine(s string) (string, int) {
	m := pathRE.FindStringSubmatch(s)
	if m == nil {
		return s, 0
	}
	if m[2] == "" {
		return m[1], 0
	}
	// m[2] is ":<digits>"
	line, err := strconv.Atoi(m[2][1:])
	if err != nil {
		return m[1], 0
	}
	return m[1], line
}

// openEditorPathPrompt enters ModeEditor with the path-input focused,
// pre-filled with the focused session's cwd + "/". If no session is
// focused we leave the field empty so the user pastes an absolute
// path.
func (m Model) openEditorPathPrompt() (tea.Model, tea.Cmd) {
	t := textinput.New()
	t.Prompt = "▸ "
	t.CharLimit = 0
	if s := m.focusedSession(); s != nil && s.Cwd != "" {
		init := s.Cwd
		if !strings.HasSuffix(init, "/") {
			init += "/"
		}
		t.SetValue(init)
		t.CursorEnd()
	}
	t.Focus()
	m.editor.pathInput = t
	m.editor.inPathPrompt = true
	m.editor.visible = true
	m.editor.err = nil
	m.mode = ModeEditor
	return m, nil
}

// loadEditorFile resolves path (relative paths are joined onto the
// focused session's cwd) and reads it asynchronously, capping at
// editorMaxBytes. The result is delivered via editorFileLoadedMsg —
// the reducer applies it on the main goroutine so all model mutations
// stay there. line, when non-zero, is stashed into scrollOffset so
// the file opens at that line.
func (m Model) loadEditorFile(path string) tea.Cmd {
	// Strip optional :<line> suffix.
	rawPath, line := splitPathLine(path)
	resolved := rawPath
	if !filepath.IsAbs(resolved) {
		base := ""
		if s := m.focusedSession(); s != nil {
			base = s.Cwd
		}
		if base == "" {
			home, _ := os.UserHomeDir()
			base = home
		}
		resolved = filepath.Join(base, resolved)
	}
	resolved = views.ExpandHome(resolved)
	scroll := 0
	if line > 1 {
		scroll = line - 1
	}
	return func() tea.Msg {
		f, err := os.Open(resolved)
		if err != nil {
			return editorFileLoadedMsg{path: resolved, err: err}
		}
		defer f.Close()
		// Read up to editorMaxBytes+1 so we can detect truncation
		// without allocating the whole file when it's much larger.
		buf := make([]byte, editorMaxBytes+1)
		n, _ := f.Read(buf)
		truncated := n > editorMaxBytes
		if truncated {
			n = editorMaxBytes
		}
		content := string(buf[:n])
		highlighted, lang := views.HighlightFile(resolved, content)
		return editorFileLoadedMsg{
			path:         resolved,
			content:      content,
			highlighted:  highlighted,
			lang:         lang,
			truncated:    truncated,
			scrollOffset: scroll,
		}
	}
}

// handleKeyEditor is the reducer for ModeEditor: path-prompt typing +
// submit, scroll commands, external-editor open, and Esc-close.
func (m Model) handleKeyEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editor.inPathPrompt {
		switch msg.String() {
		case "esc":
			m.editor.inPathPrompt = false
			m.mode = ModeMain
			// Keep the pane visible only if a file was already loaded.
			if m.editor.path == "" {
				m.editor.visible = false
			}
			return m, nil
		case "enter":
			path := strings.TrimSpace(m.editor.pathInput.Value())
			if path == "" {
				return m, nil
			}
			return m, m.loadEditorFile(path)
		}
		var cmd tea.Cmd
		m.editor.pathInput, cmd = m.editor.pathInput.Update(msg)
		return m, cmd
	}
	// Viewing mode: scroll, external open, close.
	switch msg.String() {
	case "esc":
		m.editor.visible = false
		m.mode = ModeMain
		return m, nil
	case "up", "k":
		if m.editor.scrollOffset > 0 {
			m.editor.scrollOffset--
		}
		return m, nil
	case "down", "j":
		m.editor.scrollOffset++
		return m, nil
	case "pgup":
		m.editor.scrollOffset -= 10
		if m.editor.scrollOffset < 0 {
			m.editor.scrollOffset = 0
		}
		return m, nil
	case "pgdown":
		m.editor.scrollOffset += 10
		return m, nil
	case "g":
		m.editor.scrollOffset = 0
		return m, nil
	case "G":
		// Jump to the bottom: count the highlighted line slice.
		lines := strings.Count(m.editor.highlighted, "\n")
		m.editor.scrollOffset = lines
		return m, nil
	case "ctrl+x":
		// Open in external GUI editor. We bind to Ctrl+X (per the
		// plan) because Bubble Tea's Ctrl+Shift+O reporting is
		// inconsistent across terminals.
		ed := views.DetectExternalEditor()
		if ed == nil {
			m.editor.err = fmt.Errorf(
				"no GUI editor found — set $CHUBBY_EDITOR or install pycharm/code/cursor/subl")
			return m, nil
		}
		if err := ed.OpenFile(m.editor.path, m.editor.scrollOffset+1); err != nil {
			m.editor.err = err
			return m, nil
		}
		m.toasts = append(m.toasts, toast{
			sessionName: fmt.Sprintf("opened in %s", ed.Cmd),
			color:       "10",
			expiresAt:   time.Now().Add(2 * time.Second),
		})
		return m, nil
	case "ctrl+o":
		// Re-open the path prompt while the editor is up so the user
		// can swap files without leaving the pane.
		return m.openEditorPathPrompt()
	}
	return m, nil
}

// View renders the dual-pane layout plus the compose bar, or the
// active modal pane when m.mode != ModeMain. Every mode is wrapped
// with a one-line top header and a one-line context-aware status bar
// at the bottom (see wrapWithChrome).
func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v\npress ctrl+c to quit", m.err)
	}
	switch m.mode {
	case ModeBroadcast:
		return m.wrapWithChrome(m.viewBroadcast())
	case ModeGrep:
		return m.wrapWithChrome(m.viewGrep())
	case ModeHistory:
		return m.wrapWithChrome(m.viewHistory())
	case ModeSpawn:
		return m.wrapWithChrome(m.viewSpawn())
	case ModeRename:
		return m.wrapWithChrome(m.viewRename())
	case ModeNewFolder:
		return m.wrapWithChrome(m.viewNewFolder())
	case ModeHelp:
		return m.wrapWithChrome(m.viewHelp())
	case ModeReconnecting:
		return m.wrapWithChrome(m.viewReconnecting())
	case ModeAttach:
		return m.wrapWithChrome(m.viewAttach())
	case ModeSearch:
		// Falls through to the main layout below; the rail renderer
		// adds the search bar based on m.mode == ModeSearch.
	}
	leftW := 24
	composeH := 3
	// Reserve 2 extra rows for the top header and bottom status bar.
	h := m.height - composeH - 2 - 2
	// Slash popup steals vertical space from the bottom — shrink the
	// main panes by exactly the popup's row count so nothing gets
	// pushed off-screen.
	if m.slashPopupVisible() {
		h -= len(m.slashPopupCmds)
	}
	if h < 5 {
		h = 5
	}

	// Decide the column widths. Three rendering shapes (rail/editor
	// independent on/off):
	//   rail+editor:  rail / convo+compose / editor
	//   editor only:  convo+compose / editor (50/50)
	//   rail only:    rail / convo+compose
	//   neither:      full-width convo
	railVisible := !m.railCollapsed
	editorVisible := m.editor.visible
	available := m.width
	if available < 1 {
		available = 1
	}
	var convoW, editorW int
	switch {
	case railVisible && editorVisible:
		// Reserve rail; split the rest 50/50.
		rest := available - leftW - 2
		if rest < 40 {
			rest = 40
		}
		editorW = rest / 2
		if editorW < 20 {
			editorW = 20
		}
		convoW = rest - editorW
		if convoW < 20 {
			convoW = 20
		}
	case !railVisible && editorVisible:
		// 50/50.
		rest := available - 2
		editorW = rest / 2
		if editorW < 20 {
			editorW = 20
		}
		convoW = rest - editorW
		if convoW < 20 {
			convoW = 20
		}
	case railVisible && !editorVisible:
		convoW = available - leftW - 2
		if convoW < 20 {
			convoW = 20
		}
	default:
		convoW = available - 2
		if convoW < 20 {
			convoW = 20
		}
	}

	target := "(no session)"
	color := "#888"
	if s := m.focusedSession(); s != nil {
		target = "@" + s.Name
		color = s.Color
	}

	// Render the conversation viewport and harvest any path mentions
	// before applying styling — the harvest also feeds m.editor.recentPaths
	// indirectly through View, but View can't mutate m. Tests drive the
	// path-detection via UpdateRecentPaths() called from the message
	// handlers instead.
	composeBar := views.RenderCompose(m.compose, target, color, m.composeGhost(), convoW)
	focusedSID := ""
	if s := m.focusedSession(); s != nil {
		focusedSID = s.ID
	}
	// D8: only the active pane gets the bright border. When the rail
	// is collapsed (Ctrl+J) the conversation is the only pane left, so
	// it always reads as active regardless of m.activePane.
	convoActive := m.activePane == PaneConversation || m.railCollapsed
	railActive := m.activePane == PaneRail && !m.railCollapsed
	rightCol := renderViewport(m.focusedSession(), m.conversation, convoW, h, m.spinnerFrame,
		m.scrollOffset[focusedSID], m.newSinceScroll[focusedSID], convoActive,
		m.lastUsage[focusedSID], m.thinkingStartedAt[focusedSID])
	rightStack := lipgloss.JoinVertical(lipgloss.Left, rightCol, composeBar)
	if m.slashPopupVisible() {
		popup := views.RenderSlashPopup(m.slashPopupCmds, m.slashPopupCursor, convoW)
		rightStack = lipgloss.JoinVertical(lipgloss.Left, rightStack, popup)
	}

	editorCol := ""
	if editorVisible {
		// Total editor pane height matches the conversation+compose
		// stack so the columns align visually.
		editorH := h + composeH
		editorCol = m.renderEditorPane(editorW, editorH)
	}

	var body string
	switch {
	case railVisible && editorVisible:
		rows := m.railRows()
		focusedID := ""
		if s := m.focusedSession(); s != nil {
			focusedID = s.ID
		}
		searchHeader := m.searchHeaderText()
		left := renderList(rows, m.railCursor, focusedID, m.groupCollapsed, searchHeader, leftW, h, m.spinnerFrame, railActive)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, rightStack, editorCol)
	case !railVisible && editorVisible:
		body = lipgloss.JoinHorizontal(lipgloss.Top, rightStack, editorCol)
	case railVisible && !editorVisible:
		rows := m.railRows()
		focusedID := ""
		if s := m.focusedSession(); s != nil {
			focusedID = s.ID
		}
		searchHeader := m.searchHeaderText()
		left := renderList(rows, m.railCursor, focusedID, m.groupCollapsed, searchHeader, leftW, h, m.spinnerFrame, railActive)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, rightStack)
	default:
		body = rightStack
	}
	return m.overlayToasts(m.wrapWithChrome(body))
}

// searchHeaderText returns the compact search-bar text rendered just
// below the rail's "Sessions" title — either the live textinput when
// ModeSearch is active or a "/ <query>" snippet when a filter is
// already applied.
func (m Model) searchHeaderText() string {
	if m.mode == ModeSearch {
		return m.search.query.View()
	}
	if q := m.search.query.Value(); q != "" {
		return "/ " + q
	}
	return ""
}

// renderEditorPane composes the editor column: the path-prompt
// textinput (when inPathPrompt) or the highlighted file body.
func (m Model) renderEditorPane(w, h int) string {
	if m.editor.inPathPrompt {
		dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).
			Render("Open file")
		body := title + "\n\n" + m.editor.pathInput.View() + "\n\n"
		if m.editor.err != nil {
			body += lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
				Render("error: "+m.editor.err.Error()) + "\n\n"
		}
		body += dim.Render("Enter to load · Esc to cancel")
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Width(w - 2).Height(h - 2).
			Padding(0, 1).
			Render(body)
	}
	pane := views.EditorPaneState{
		Path:         m.editor.path,
		Highlighted:  m.editor.highlighted,
		Lang:         m.editor.lang,
		ScrollOffset: m.editor.scrollOffset,
		Err:          m.editor.err,
		Truncated:    m.editor.truncated,
	}
	return views.RenderEditor(pane, w, h)
}

// wrapWithChrome prepends the minimal top header and appends the
// context-aware status bar to a rendered body. Used by every mode so
// the chrome stays visible regardless of which modal is active.
func (m Model) wrapWithChrome(body string) string {
	composeHasText := m.compose.Value() != ""
	idle := 0
	for _, s := range m.sessions {
		if s.Status == "awaiting_user" {
			idle++
		}
	}
	header := views.TopStatus("", len(m.sessions), idle, m.width)
	status := views.StatusBarText(
		views.StatusMode(m.mode),
		composeHasText,
		m.bcast.field,
		m.attach.confirmDetachN > 0,
		m.width,
	)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status)
}

// overlayToasts renders the current toasts and appends them to the
// bottom-right of body. Bubble Tea has no real layered compositor, so
// this approximates the spec by stacking toasts in a right-aligned
// block beneath the main view (still on-screen, still attention-getting,
// preserves the rest of the layout).
func (m Model) overlayToasts(body string) string {
	if len(m.toasts) == 0 {
		return body
	}
	var stack strings.Builder
	for _, t := range m.toasts {
		line := fmt.Sprintf("⚡ %s awaiting", t.sessionName)
		styled := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Foreground(lipgloss.Color(t.color)).
			Render(line)
		stack.WriteString(styled + "\n")
	}
	if m.width > 0 {
		// Right-align the block within the terminal width.
		placed := lipgloss.PlaceHorizontal(m.width, lipgloss.Right, stack.String())
		return body + "\n" + placed
	}
	return body + "\n" + stack.String()
}

func (m Model) viewBroadcast() string {
	w := m.width
	if w < 40 {
		w = 40
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(
		"Broadcast (Tab=switch field, Space=toggle, a=all, n=none, i=invert, Esc=cancel)") + "\n\n")

	listStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(w - 4)
	if m.bcast.field == 0 {
		listStyle = listStyle.BorderForeground(lipgloss.Color("12"))
	}
	var ll strings.Builder
	for i, s := range m.sessions {
		mark := "[ ]"
		if m.bcast.selected[s.ID] {
			mark = "[x]"
		}
		cursor := "  "
		if i == m.bcast.cursor && m.bcast.field == 0 {
			cursor = "▸ "
		}
		col := lipgloss.NewStyle().Foreground(lipgloss.Color(s.Color)).Render(s.Name)
		ll.WriteString(fmt.Sprintf("%s%s %s\n", cursor, mark, col))
	}
	b.WriteString(listStyle.Render(ll.String()) + "\n")

	textStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(w - 4).Height(6)
	if m.bcast.field == 1 {
		textStyle = textStyle.BorderForeground(lipgloss.Color("12"))
	}
	body := m.bcast.text + "_"
	if g := slashGhost(m.bcast.text); g != "" {
		body = m.bcast.text +
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(g) +
			"_"
	}
	b.WriteString(textStyle.Render(body) + "\n")
	// Below the textarea: a small dim hint line previewing the first
	// slash-completion match. Mirrors the compose-bar ghost so users
	// know Tab is wired here too.
	if hint := slashGhost(m.bcast.text); hint != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
			Render("  Tab → "+m.bcast.text+hint) + "\n")
	}

	btnStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	if m.bcast.field == 2 {
		btnStyle = btnStyle.BorderForeground(lipgloss.Color("12")).Bold(true)
	}
	b.WriteString(btnStyle.Render(" Send "))
	return b.String()
}

func (m Model) viewGrep() string {
	w := m.width
	if w < 40 {
		w = 40
	}
	resH := m.height - 8
	if resH < 5 {
		resH = 5
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(
		"Grep transcripts (Esc=cancel, Enter=jump to session)") + "\n\n")
	qStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(w - 4)
	b.WriteString(qStyle.Render(m.grep.query.View()) + "\n\n")

	resStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		Width(w - 4).Height(resH)
	var rs strings.Builder
	for i, r := range m.grep.results {
		cursor := "  "
		if i == m.grep.cursor {
			cursor = "▸ "
		}
		role, _ := r["role"].(string)
		snippet, _ := r["snippet"].(string)
		ts, _ := r["ts"].(float64)
		t := time.UnixMilli(int64(ts)).Format("01-02 15:04")
		line := fmt.Sprintf("%s%s [%s] %s", cursor, t, role, snippet)
		if len(line) > w-6 {
			line = line[:w-6]
		}
		rs.WriteString(line + "\n")
	}
	b.WriteString(resStyle.Render(rs.String()))
	return b.String()
}

func (m Model) viewHistory() string {
	w := m.width
	if w < 60 {
		w = 60
	}
	colW := (w - 6) / 3
	h := m.height - 4
	if h < 8 {
		h = 8
	}
	var runsCol strings.Builder
	runsCol.WriteString(lipgloss.NewStyle().Bold(true).Render(" Hub runs") + "\n")
	for i, r := range m.history.runs {
		cursor := "  "
		if i == m.history.runCursor {
			cursor = "▸ "
		}
		id, _ := r["id"].(string)
		ts, _ := r["started_at"].(float64)
		t := time.UnixMilli(int64(ts)).Format("01-02 15:04")
		short := id
		if len(short) > 8 {
			short = short[:8]
		}
		runsCol.WriteString(fmt.Sprintf("%s%s %s\n", cursor, t, short))
	}

	var sessCol strings.Builder
	sessCol.WriteString(lipgloss.NewStyle().Bold(true).Render(" Sessions") + "\n")
	for i, s := range m.history.runSessions {
		cursor := "  "
		if i == m.history.sessCursor {
			cursor = "▸ "
		}
		col := lipgloss.NewStyle().Foreground(lipgloss.Color(s.Color)).Render(s.Name)
		sessCol.WriteString(fmt.Sprintf("%s%s\n", cursor, col))
	}

	var prevCol strings.Builder
	prevCol.WriteString(lipgloss.NewStyle().Bold(true).Render(" Log preview") + "\n")
	prevCol.WriteString(m.history.preview)

	border := func(s string, focused bool) string {
		st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			Width(colW).Height(h)
		if focused {
			st = st.BorderForeground(lipgloss.Color("12"))
		}
		return st.Render(s)
	}
	cols := lipgloss.JoinHorizontal(lipgloss.Top,
		border(runsCol.String(), m.history.column == 0),
		border(sessCol.String(), m.history.column == 1),
		border(prevCol.String(), m.history.column == 2),
	)
	header := lipgloss.NewStyle().Bold(true).Render(
		"History (Tab=switch col, Enter=open, Esc=close)")
	return lipgloss.JoinVertical(lipgloss.Left, header, cols)
}

func (m Model) viewHelp() string {
	body := `chubby-tui keys

  Tab                switch active pane (rail / conversation)
  Ctrl+\             cycle focused session forward
  Shift+Tab          cycle focused session (reverse)
  Up/Down/PgUp/PgDn  scroll active pane
  Home/End           jump to top/bottom of active pane
  Space              toggle folder collapse
  Enter              rail-pane: focus session / toggle folder
                     conversation-pane: send composed message
  @name <msg>        one-shot redirect: send to <name>, then snap back
  Tab (in compose)   autocomplete @name or /command
  /<name>            Claude slash command (Tab completes; e.g. /model sonnet)
  /color #RRGGBB     (chubby) recolor focused session
  /rename <name>     (chubby) rename focused session
  /tag +foo -bar     (chubby) modify tags
  /refresh-claude    (chubby) restart claude with --resume (picks up settings changes)

  Ctrl+N             new session in focused cwd
  Ctrl+F             new folder
  Ctrl+A             attach existing claude sessions (multi-select picker;
                     d on selected → detach from chubby with confirm)
  Ctrl+K             search session list
  Ctrl+R             rename focused session OR folder (rail cursor)
  Ctrl+P             respawn focused dead session
  Ctrl+B             broadcast modal
  Ctrl+G             grep transcripts (current run)
  Ctrl+H             history panel (past hub-runs)
  Ctrl+J             toggle rail (full-width conversation)
  Ctrl+Y             copy focused session's conversation to clipboard

  Ctrl+O             open file viewer (path prompt)
  Ctrl+]             open most-recent path mentioned in conversation
  Ctrl+E             toggle editor pane
  Ctrl+X (in editor) open file in external editor (PyCharm/VSCode/...)

  /movetofolder X    (chubby) move focused session to folder X (creates if new)
  /removefromfolder  (chubby) remove focused session from its folder
  /detach            release session from chubby; opens a new terminal with claude --resume

  Tab (in cwd)       complete directory path; cycle on repeat
  Ctrl+P (in cwd)    cycle recent cwds

  ?                  this help
  q / Ctrl+C         quit`
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		Render(body)
	w, h := m.width, m.height
	if w < 1 {
		w = 60
	}
	if h < 1 {
		h = 20
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) viewSpawn() string {
	w := m.width
	if w < 50 {
		w = 50
	}
	cw := w - 12
	if cw < 30 {
		cw = 30
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	highlight := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	label := func(text string, active bool) string {
		if active {
			return highlight.Render("▸ " + text)
		}
		return dim.Render("  " + text)
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("New session") + "\n\n")
	b.WriteString(label("name:   ", m.spawn.field == 0) + m.spawn.name.View() + "\n")
	b.WriteString(label("cwd:    ", m.spawn.field == 1) + m.spawn.cwd.View() + "\n")
	b.WriteString(label("folder: ", m.spawn.field == 2) + m.spawn.folder.View() + "\n\n")
	if m.spawn.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Render("error: "+m.spawn.err.Error()) + "\n\n")
	}
	b.WriteString(dim.Render(
		"Tab switch field/path · Ctrl+P recent · Enter spawn · Esc cancel"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(cw).
		Padding(0, 1).
		Render(b.String())
	wh, hh := m.width, m.height
	if wh < 1 {
		wh = w
	}
	if hh < 1 {
		hh = 10
	}
	return lipgloss.Place(wh, hh, lipgloss.Center, lipgloss.Center, box)
}

// viewRename renders the centered rename modal, used for both the
// session and group targets.
func (m Model) viewRename() string {
	w := m.width
	if w < 50 {
		w = 50
	}
	cw := w - 12
	if cw < 30 {
		cw = 30
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	title := "Rename session"
	switch m.rename.target {
	case RenameGroup:
		title = "Rename group"
	case RenameFolder:
		title = "Rename folder"
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(title) + "\n\n")
	b.WriteString(dim.Render("  old: ") + m.rename.oldName + "\n")
	b.WriteString("  new: " + m.rename.input.View() + "\n\n")
	if m.rename.target == RenameGroup {
		b.WriteString(dim.Render(fmt.Sprintf(
			"Will retag %d sessions", len(m.rename.sessions))) + "\n")
	}
	b.WriteString(dim.Render("Enter to apply · Esc cancel"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(cw).
		Padding(0, 1).
		Render(b.String())
	wh, hh := m.width, m.height
	if wh < 1 {
		wh = w
	}
	if hh < 1 {
		hh = 10
	}
	return lipgloss.Place(wh, hh, lipgloss.Center, lipgloss.Center, box)
}

// viewAttach renders the multi-select attach picker: a list of
// scan_candidates rows with a checkbox, the cursor highlight, and a
// per-row classification glyph. Already-attached rows are dimmed and
// skipped by 'a'. The bottom-of-modal notice line surfaces transient
// feedback ("attached N sessions") or scan errors.
func (m Model) viewAttach() string {
	w := m.width
	if w < 60 {
		w = 60
	}
	var b strings.Builder
	bold := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	cursorRow := lipgloss.NewStyle().Background(lipgloss.Color("237"))

	b.WriteString(bold.Render("Attach existing Claude sessions") + "\n")
	switch {
	case m.attach.loading:
		b.WriteString(dim.Render("Loading...") + "\n")
	case m.attach.err != nil:
		b.WriteString(red.Render("scan failed: "+m.attach.err.Error()) + "\n")
	case len(m.attach.candidates) == 0:
		b.WriteString(dim.Render("(no candidates)") + "\n")
	default:
		b.WriteString("\n")
		for i, c := range m.attach.candidates {
			classification, _ := c["classification"].(string)
			cwd, _ := c["cwd"].(string)
			pidF, _ := c["pid"].(float64)
			pid := int(pidF)
			tmuxTarget, _ := c["tmux_target"].(string)
			alreadyAttached, _ := c["already_attached"].(bool)

			cursorMark := "  "
			if i == m.attach.cursor {
				cursorMark = "▸ "
			}
			box := "[ ]"
			if m.attach.selected[i] {
				box = "[x]"
			}

			var classGlyph string
			switch {
			case alreadyAttached:
				classGlyph = dim.Render("• already attached")
			case classification == "tmux_full":
				suffix := ""
				if tmuxTarget != "" {
					suffix = "  (" + tmuxTarget + ")"
				}
				classGlyph = green.Render("✓ tmux") + dim.Render(suffix)
			case classification == "promote_required":
				classGlyph = yellow.Render("⚠ readonly only")
			default:
				classGlyph = dim.Render("· " + classification)
			}

			label := fmt.Sprintf("claude pid %d  %s", pid, cwd)
			if alreadyAttached {
				label = dim.Render(label)
			}
			line := fmt.Sprintf("%s%s %s   %s", cursorMark, box, label, classGlyph)
			if i == m.attach.cursor {
				line = cursorRow.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}

	if m.attach.confirmDetachN > 0 {
		// Bright prompt — the user is about to release sessions, surface
		// it loudly. Plural-aware copy reads naturally on N=1 too.
		noun := "session"
		if m.attach.confirmDetachN != 1 {
			noun = "sessions"
		}
		warn := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
		b.WriteString("\n" + warn.Render(fmt.Sprintf(
			"Detach %d %s? (y/n)", m.attach.confirmDetachN, noun)))
	}
	if m.attach.notice != "" {
		b.WriteString("\n" + dim.Render(m.attach.notice))
	}
	if m.attach.err != nil && !m.attach.loading {
		b.WriteString("\n" + red.Render("error: "+m.attach.err.Error()))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(w - 4).
		Render(b.String())
	wh, hh := m.width, m.height
	if wh < 1 {
		wh = w
	}
	if hh < 1 {
		hh = 20
	}
	return lipgloss.Place(wh, hh, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) viewReconnecting() string {
	msg := fmt.Sprintf("reconnecting to chubbyd... (attempt %d)", m.reconnectAttempts)
	w, h := m.width, m.height
	if w < 1 {
		w = 40
	}
	if h < 1 {
		h = 10
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().Bold(true).Render(msg))
}

func (m Model) focusedSession() *Session {
	if m.focused < 0 || m.focused >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.focused]
}

// focusedSessionID returns the focused session's id, or "" if there
// is no focused session. Convenience for the scroll helpers which key
// every operation by session id.
func (m Model) focusedSessionID() string {
	if s := m.focusedSession(); s != nil {
		return s.ID
	}
	return ""
}

// maxScrollFor reports the maximum scroll offset (in lines) for the
// given session, based on the most-recently-rendered viewport
// geometry. If the conversation fits entirely on screen, max is 0
// (nowhere to scroll). Recomputes the wrapped line count on demand
// from the cached innerW so callers don't depend on viewMain having
// run since the last conversation mutation.
func (m Model) maxScrollFor(sid string) int {
	if m.lastViewportInnerH <= 0 || m.lastViewportInnerW <= 0 {
		// We haven't rendered yet; keep the offset whatever it is so
		// the first render establishes geometry. Returning a huge
		// number would let scrollUp run away before render lands.
		return m.scrollOffset[sid]
	}
	turns := m.conversation[sid]
	if len(turns) == 0 {
		return 0
	}
	// "color" doesn't affect line count, only ANSI bytes — we can pass
	// any string. Color the focused session's color when available so
	// downstream styling is consistent if the test checks the rendered
	// output too.
	color := "12"
	for i := range m.sessions {
		if m.sessions[i].ID == sid {
			color = m.sessions[i].Color
			break
		}
	}
	body := renderTurns(turns, color, m.lastViewportInnerW)
	// renderTurns trailing-newlines each turn, so split on "\n".
	lc := strings.Count(body, "\n")
	// 2 rows of banner + spacer subtract from the visible body area;
	// matches the layout in renderViewport (header + "\n\n").
	visible := m.lastViewportInnerH - 2
	if visible < 1 {
		visible = 1
	}
	max := lc - visible
	if max < 0 {
		return 0
	}
	return max
}

// scrollUp moves the focused session's scroll position UP by n lines
// (toward older messages), clamped to maxScrollFor. No-op if no
// session is focused.
func (m *Model) scrollUp(n int) {
	sid := m.focusedSessionID()
	if sid == "" {
		return
	}
	max := m.maxScrollFor(sid)
	cur := m.scrollOffset[sid] + n
	if cur > max {
		cur = max
	}
	if cur < 0 {
		cur = 0
	}
	m.scrollOffset[sid] = cur
}

// scrollDown moves the focused session's scroll position DOWN by n
// lines (toward the latest message), clamped to 0. When the user
// arrives back at the bottom (offset==0), unread-count is cleared so
// the "↓ N new" indicator goes away.
func (m *Model) scrollDown(n int) {
	sid := m.focusedSessionID()
	if sid == "" {
		return
	}
	cur := m.scrollOffset[sid] - n
	if cur < 0 {
		cur = 0
	}
	m.scrollOffset[sid] = cur
	if cur == 0 {
		m.newSinceScroll[sid] = 0
	}
}

// scrollToTop pins the focused session's view to the oldest visible
// line (max offset). Bound to "home" / "g g".
func (m *Model) scrollToTop() {
	sid := m.focusedSessionID()
	if sid == "" {
		return
	}
	m.scrollOffset[sid] = m.maxScrollFor(sid)
}

// scrollToBottom pins the focused session's view to the latest line
// (offset 0) and clears the unread-count. Bound to "end" / "G".
func (m *Model) scrollToBottom() {
	sid := m.focusedSessionID()
	if sid == "" {
		return
	}
	m.scrollOffset[sid] = 0
	m.newSinceScroll[sid] = 0
}

// recomputeViewportGeom recalculates the conversation pane's inner
// width and height from the current m.width / m.height, mirroring the
// layout math in viewMain. We cache these on the model so the scroll
// helpers can answer "max offset" without re-running the layout —
// and so they work even before the first View() call lands. Called
// from WindowSizeMsg and from listMsg (in case the rail visibility
// changed before any resize arrived).
func (m *Model) recomputeViewportGeom() {
	leftW := 24
	composeH := 3
	h := m.height - composeH - 2 - 2
	if m.slashPopupVisible() {
		h -= len(m.slashPopupCmds)
	}
	if h < 5 {
		h = 5
	}
	railVisible := !m.railCollapsed
	editorVisible := m.editor.visible
	available := m.width
	if available < 1 {
		available = 1
	}
	var convoW int
	switch {
	case railVisible && editorVisible:
		rest := available - leftW - 2
		if rest < 40 {
			rest = 40
		}
		editorW := rest / 2
		if editorW < 20 {
			editorW = 20
		}
		convoW = rest - editorW
		if convoW < 20 {
			convoW = 20
		}
	case !railVisible && editorVisible:
		rest := available - 2
		editorW := rest / 2
		if editorW < 20 {
			editorW = 20
		}
		convoW = rest - editorW
		if convoW < 20 {
			convoW = 20
		}
	case railVisible && !editorVisible:
		convoW = available - leftW - 2
		if convoW < 20 {
			convoW = 20
		}
	default:
		convoW = available - 2
		if convoW < 20 {
			convoW = 20
		}
	}
	// renderViewport draws inside a rounded border (1 col each side),
	// so the inner width for wrapping is convoW - 2.
	m.lastViewportInnerW = convoW - 2
	m.lastViewportInnerH = h
}

// halfViewportPage returns the page-step for pgup/pgdown — half the
// viewport's visible inner height, with a sensible floor so we always
// move by at least a few lines even before the first render.
func (m Model) halfViewportPage() int {
	h := m.lastViewportInnerH / 2
	if h < 5 {
		h = 5
	}
	return h
}

// clampAllScrollOffsets re-applies maxScrollFor to every per-session
// offset. Called from tea.WindowSizeMsg so a terminal resize that
// shrinks the viewport doesn't leave stale offsets pointing past the
// new max.
func (m *Model) clampAllScrollOffsets() {
	for sid, off := range m.scrollOffset {
		max := m.maxScrollFor(sid)
		if off > max {
			m.scrollOffset[sid] = max
		}
		if off < 0 {
			m.scrollOffset[sid] = 0
		}
	}
}

// transcriptDedupWindow is how many trailing turns we scan when
// deciding whether a freshly-arrived transcript_message is a duplicate.
// 5 is enough to absorb the daemon's tailer replay racing the
// get_session_history seed without paying for a long scan on every
// live event, and short enough that legitimately-repeated content
// further back in the conversation still appends.
const transcriptDedupWindow = 5

// appendTranscriptTurn appends a live transcript turn to the per-
// session conversation, deduping against the last few entries.
//
// Why dedup at all: when the TUI loads a session whose JSONL is being
// tailed by the daemon, the tailer reads from offset 0 and broadcasts
// every existing turn as a transcript_message event. The TUI ALSO
// calls get_session_history and replaces m.conversation[sid] with the
// loaded turns. If live events arrive after the replacement (the
// common race), those turns get appended a second time and the user
// sees duplicates. The historyTurnsMsg REPLACE path is the canonical
// seed and is intentionally NOT deduped — only live events run through
// here.
func (m *Model) appendTranscriptTurn(sid, role, text string, ts int64) {
	turns := m.conversation[sid]
	n := len(turns)
	start := n - transcriptDedupWindow
	if start < 0 {
		start = 0
	}
	for i := start; i < n; i++ {
		if turns[i].Role == role && turns[i].Text == text {
			return
		}
	}
	turns = append(turns, Turn{Role: role, Text: text, Ts: ts})
	if len(turns) > turnsCap {
		turns = turns[len(turns)-turnsCap:]
	}
	m.conversation[sid] = turns
}

// formatConversation renders turns as a single string for clipboard
// export: turns separated by blank lines, user prompts prefixed with
// "▸ " to mirror the in-TUI viewport. Pure (no I/O) so tests can
// assert against it without touching the system clipboard.
func formatConversation(turns []Turn) string {
	var b strings.Builder
	for i, t := range turns {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if t.Role == "user" {
			b.WriteString("▸ ")
		}
		b.WriteString(t.Text)
	}
	return b.String()
}

// copyConversation returns a tea.Cmd that copies the focused session's
// conversation to the system clipboard. Returns nil (no-op) when there
// is no focused session or it has no turns. On success, emits
// copiedMsg{count} so the reducer can show a transient toast.
func (m Model) copyConversation() tea.Cmd {
	s := m.focusedSession()
	if s == nil {
		return nil
	}
	turns := m.conversation[s.ID]
	if len(turns) == 0 {
		return nil
	}
	payload := formatConversation(turns)
	count := len(turns)
	return func() tea.Msg {
		if err := clipboard.WriteAll(payload); err != nil {
			return errMsg{err}
		}
		return copiedMsg{count: count}
	}
}

// railRows returns the visible left-rail rows for the current model
// state, taking the search filter and group-collapse map into account.
func (m Model) railRows() []RailRow {
	visible := m.visibleSessions()
	return BuildRailRows(visible, m.sessions, m.groupCollapsed, m.folders)
}

// visibleSessions applies the search filter. With no filter active
// this is m.sessions verbatim.
func (m Model) visibleSessions() []Session {
	return filterSessions(m.sessions, m.search.query.Value())
}

// railSessionRows is the subset of rail rows that are sessions, in rail
// order — used by Tab/Shift+Tab to cycle session-only.
func (m Model) railSessionRows() []RailRow {
	rows := m.railRows()
	out := make([]RailRow, 0, len(rows))
	for _, r := range rows {
		if r.Kind == RailRowSession {
			out = append(out, r)
		}
	}
	return out
}

// syncRailCursorToFocus moves m.railCursor onto the row that
// represents m.focused, if visible. If not visible (e.g. inside a
// collapsed group), the cursor is left alone.
func (m *Model) syncRailCursorToFocus() {
	rows := m.railRows()
	for i, r := range rows {
		if r.Kind == RailRowSession && r.SessionIdx == m.focused {
			m.railCursor = i
			return
		}
	}
}

// focusRailRow updates m.focused if the rail cursor is on a session.
// Headers leave m.focused alone.
func (m *Model) focusRailRow() {
	rows := m.railRows()
	if m.railCursor < 0 || m.railCursor >= len(rows) {
		return
	}
	r := rows[m.railCursor]
	if r.Kind == RailRowSession {
		m.focused = r.SessionIdx
	}
}

// tryComplete attempts to autocomplete a trailing "@<partial>" in the
// compose bar. Returns true if it actually mutated the input. With one
// match it inserts the full name + space. With multiple matches it
// cycles via m.completionIndex on repeated Tab presses.
func (m *Model) tryComplete() bool {
	val := m.compose.Value()
	partial, _, ok := extractTrailingAt(val)
	if !ok {
		return false
	}
	matches := matchSessionNames(m.sessions, partial)
	if len(matches) == 0 {
		return false
	}
	if m.completionPartial != partial {
		m.completionIndex = 0
		m.completionPartial = partial
	} else {
		m.completionIndex = (m.completionIndex + 1) % len(matches)
	}
	chosen := matches[m.completionIndex]
	newVal, did := completeAt(val, chosen)
	if !did {
		return false
	}
	m.compose.SetValue(newVal)
	m.compose.CursorEnd()
	// Keep completionPartial set so a subsequent Tab on the same partial
	// (after the user backspaces back to "@<partial>") cycles forward.
	return true
}

// composeGhost returns the dim suffix to render after the compose
// textinput's content: shows the next completion match for the
// trailing "@<partial>" or "/<cmd-or-arg-partial>". Empty string means
// no ghost.
func (m Model) composeGhost() string {
	val := m.compose.Value()
	// Slash command / arg ghost takes priority — a leading "/" is
	// unambiguous, and the @-mention regex won't match a slash anyway.
	if g := slashGhost(val); g != "" {
		return g
	}
	partial, _, ok := extractTrailingAt(val)
	if !ok || partial == "" {
		return ""
	}
	matches := matchSessionNames(m.sessions, partial)
	if len(matches) == 0 {
		return ""
	}
	pick := matches[0]
	if len(pick) <= len(partial) {
		return ""
	}
	return pick[len(partial):]
}

// cycleFocusedSession advances focus by `dir` (+1 or -1) over the
// session-only rows in rail order, skipping group headers and
// collapsed groups. Updates the rail cursor too.
func (m *Model) cycleFocusedSession(dir int) {
	sessRows := m.railSessionRows()
	if len(sessRows) == 0 {
		return
	}
	// Find current focus position in sessRows.
	cur := -1
	for i, r := range sessRows {
		if r.SessionIdx == m.focused {
			cur = i
			break
		}
	}
	var next int
	if cur >= 0 {
		next = (cur + dir + len(sessRows)) % len(sessRows)
	} else {
		// Focused session isn't in the visible rail (filter-hidden or
		// inside a collapsed group). Default to the first visible session.
		next = 0
	}
	m.focused = sessRows[next].SessionIdx
	m.syncRailCursorToFocus()
}

// moveRailCursor walks the rail rows by `dir` (+1 or -1). Separator
// rows (RailRowUnfiledSeparator) are skipped — they're decorative and
// the cursor should never rest on them. When the cursor lands on a
// session, m.focused is updated; landing on a folder header leaves the
// focused session alone.
func (m *Model) moveRailCursor(dir int) {
	rows := m.railRows()
	if len(rows) == 0 {
		return
	}
	n := len(rows)
	// Walk at most n steps so we don't loop forever if every row is a
	// separator (shouldn't happen, but defensive).
	next := m.railCursor
	for step := 0; step < n; step++ {
		next = (next + dir + n) % n
		if rows[next].Kind != RailRowUnfiledSeparator {
			break
		}
	}
	m.railCursor = next
	m.focusRailRow()
}

// spinnerFrames is the Braille-dot spinner cycle used to indicate that
// a session is "thinking" — the Claude wrapper has been injected to and
// hasn't replied yet. Bright yellow (color 11) so it pops next to dim
// idle dots in the rail.
const spinnerFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

var spinnerRunes = []rune(spinnerFrames)

var spinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)

// statusGlyph returns the rail/banner glyph for a session's status.
// For "thinking", frame indexes into the spinner cycle so successive
// renders animate; everything else is a static glyph. The returned
// string is already styled (color/bold) — callers should not wrap it
// in extra Foreground styles for "thinking" or they'll fight the
// vivid-yellow accent that distinguishes a working session from idle.
func statusGlyph(status string, frame int) string {
	switch status {
	case "thinking":
		return spinnerStyle.Render(string(spinnerRunes[frame%len(spinnerRunes)]))
	case "awaiting_user":
		return "⚡"
	case "dead":
		return "✕"
	case "idle":
		return "○"
	default:
		return "·"
	}
}

// activePaneBorderColor / inactivePaneBorderColor are the lipgloss
// color codes used for the focused vs unfocused border of the rail
// and conversation panes (D8). 12 is the bright-blue accent already
// used elsewhere for "active" highlights; 240 is the dim grey we use
// for chrome we don't want competing for attention.
const (
	activePaneBorderColor   = lipgloss.Color("12")
	inactivePaneBorderColor = lipgloss.Color("240")
)

// renderList draws the grouped left rail. rows is the flattened
// header+session list from BuildRailRows; cursor is the highlighted
// row; focusedID is the currently-focused session's ID (gets the ▣
// marker even if it's not the cursor row); searchHeader (optional) is
// rendered just below the "Sessions" title so the user sees the active
// filter; active toggles the border color so the user sees which pane
// owns arrow / paging keys (D8).
func renderList(rows []RailRow, cursor int, focusedID string, collapsed map[string]bool, searchHeader string, w, h, spinnerFrame int, active bool) string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(" Sessions") + "\n")
	if searchHeader != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("12")).
			Render(" "+searchHeader) + "\n")
	}
	separatorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
	folderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	for i, r := range rows {
		switch r.Kind {
		case RailRowUnfiledSeparator:
			// Dim, non-interactive separator between the folder block
			// and the unfiled-sessions block. Skipped by cursor
			// navigation in moveRailCursor.
			b.WriteString(separatorStyle.Render("  "+r.GroupName) + "\n")
		case RailRowFolder:
			// Folders use a 📁 glyph; honor the collapsed state via the
			// same shared map used elsewhere.
			glyph := "📁"
			if collapsed[r.GroupName] {
				glyph = "📁▸"
			}
			cursorMark := " "
			if i == cursor {
				cursorMark = ">"
			}
			line := fmt.Sprintf("%s %s %s", cursorMark, glyph, r.GroupName)
			b.WriteString(folderStyle.Render(line) + "\n")
		case RailRowSession:
			s := r.Session
			marker := "  "
			if s.ID == focusedID {
				marker = "▣ "
			} else if i == cursor {
				marker = "▸ "
			}
			col := lipgloss.Color(s.Color)
			glyph := statusGlyph(s.Status, spinnerFrame)
			line := fmt.Sprintf("  %s%s %s", marker,
				lipgloss.NewStyle().Foreground(col).Render(s.Name),
				glyph)
			b.WriteString(lipgloss.NewStyle().Width(w).Render(line) + "\n")
		}
	}
	borderColor := inactivePaneBorderColor
	if active {
		borderColor = activePaneBorderColor
	}
	return lipgloss.NewStyle().
		Width(w).Height(h).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Render(b.String())
}

// renderSessionBanner builds the colored top-of-viewport header. The
// banner is a TWO-LINE block:
//
//	┃ <name>  ●  <cwd> · <kind>
//	  <spinner> 1m 23s · ↓ 2.0k tokens · cogitating ▰▰▰▱▱▱▱▱▱▱
//
// or, when not thinking but with usage available:
//
//	┃ <name>  ●  <cwd> · <kind> · idle
//	  ↑ 12.3k ↓ 2.0k tokens · cache 999
//
// The bar (U+2503) and session-name are rendered in the session's color
// (bold). The dot (U+25CF) acts as a swatch, also in the session's color.
// cwd / kind / status are dimmed so they read as metadata rather than
// content.
//
// scrolledUp adds a "· scrolled up · End to jump down" suffix in
// yellow so the user sees that the viewport is no longer pinned to
// the bottom even if the new-messages badge isn't visible (e.g. they
// scrolled up but no new messages have arrived yet).
//
// usage / thinkingStartedAt / isThinking / spinnerFrame drive the
// activity line. When isThinking is true, we show the live elapsed
// counter + token rate slider; otherwise we show static usage totals
// (only if any tokens have been observed for this session).
func renderSessionBanner(
	s *Session,
	spinnerFrame int,
	scrolledUp bool,
	usage sessionUsage,
	thinkingStartedAt time.Time,
	isThinking bool,
) string {
	col := lipgloss.Color(s.Color)
	colorStyle := lipgloss.NewStyle().Foreground(col).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	bar := colorStyle.Render("┃")
	name := colorStyle.Render(s.Name)
	swatch := lipgloss.NewStyle().Foreground(col).Render("●")

	// Line 1: identity (bar/name/swatch + cwd · kind). When NOT
	// thinking we also append the static status here so the second
	// line is free to show usage totals without restating "idle".
	line1 := fmt.Sprintf("%s %s  %s  %s",
		bar, name, swatch,
		dim.Render(fmt.Sprintf("%s · %s", s.Cwd, s.Kind)))
	if !isThinking {
		line1 += dim.Render(" · " + s.Status)
	}
	if scrolledUp {
		hint := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).
			Render(" · scrolled up · End to jump down")
		line1 += hint
	}

	// Line 2: activity. Branches on isThinking + whether we have
	// recorded any usage at all. Returning just line1 is allowed when
	// there's nothing useful to show on line 2 (no tokens yet, idle).
	line2 := buildBannerActivityLine(s, spinnerFrame, usage, thinkingStartedAt, isThinking)
	if line2 == "" {
		return line1
	}
	return line1 + "\n  " + line2
}

// buildBannerActivityLine constructs the 2nd banner line. Returns ""
// when there's nothing to render (no usage data + not thinking) so
// the caller can fall back to the original single-line banner shape.
func buildBannerActivityLine(
	s *Session,
	spinnerFrame int,
	usage sessionUsage,
	thinkingStartedAt time.Time,
	isThinking bool,
) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	col := lipgloss.Color(s.Color)
	hasUsage := usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.CacheReadInputTokens > 0
	if !isThinking && !hasUsage {
		return ""
	}
	if isThinking {
		var elapsed time.Duration
		if !thinkingStartedAt.IsZero() {
			elapsed = time.Since(thinkingStartedAt)
		}
		viewSamples := make([]views.UsageSample, 0, len(usage.samples))
		for _, sm := range usage.samples {
			viewSamples = append(viewSamples, views.UsageSample{
				Ts: sm.Ts, OutputTokens: sm.OutputTokens,
			})
		}
		tps := views.TokensPerSecond(viewSamples, time.Now(), 2*time.Second)
		// Reuse the existing thinking-spinner glyph so the second
		// line's leader matches the rail; statusGlyph styles it for us.
		spinner := statusGlyph("thinking", spinnerFrame)
		elapsedStr := views.FormatElapsed(elapsed)
		tokensStr := views.FormatTokens(usage.OutputTokens)
		statusText := views.ThinkingStatusText(elapsed, tps)
		slider := lipgloss.NewStyle().Foreground(col).
			Render(views.RenderSlider(tps, 10))
		// "  spinner Xs · ↓ Yk tokens · status ▰▰…"
		return fmt.Sprintf(
			"%s %s · %s · %s %s",
			spinner,
			dim.Render(elapsedStr),
			dim.Render("↓ "+tokensStr+" tokens"),
			dim.Render(statusText),
			slider,
		)
	}
	// Idle / awaiting — show static totals only.
	parts := []string{
		fmt.Sprintf("↑ %s", views.FormatTokens(usage.InputTokens)),
		fmt.Sprintf("↓ %s tokens", views.FormatTokens(usage.OutputTokens)),
	}
	if usage.CacheReadInputTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache %s",
			views.FormatTokens(usage.CacheReadInputTokens)))
	}
	return dim.Render(strings.Join(parts, " · "))
}

// viewportRender is the result of renderViewport: the framed string to
// place into the layout, plus the wrapped-line count of the rendered
// transcript body (excluding the banner) — viewMain stashes the line
// count into m.lastViewportLineCount so the scroll-clamping helpers
// can answer "how far up?" without re-running the wrap.
type viewportRender struct {
	view      string
	lineCount int
}

// renderViewport draws the focused session's structured conversation:
// a colored session banner, then user prompts marked with a coloured
// arrow and assistant responses in the default fg, separated by blank
// lines. The previous implementation rendered the raw PTY byte stream,
// which was unreadable inside lipgloss because Claude's cursor-
// positioning escapes don't compose with lipgloss frames.
//
// scrollOffset slides the visible window UP by that many lines (0 =
// pinned to bottom — the historical behavior). newCount, when > 0 and
// scrollOffset > 0, renders a yellow "↓ N new messages · End to jump"
// badge bottom-right inside the frame; the banner also gets a
// "scrolled up" hint so the state is obvious without looking at the
// badge.
func renderViewport(
	s *Session,
	conversation map[string][]Turn,
	w, h, spinnerFrame, scrollOffset, newCount int,
	active bool,
	usage sessionUsage,
	thinkingStartedAt time.Time,
) string {
	r := renderViewportFull(s, conversation, w, h, spinnerFrame,
		scrollOffset, newCount, active, usage, thinkingStartedAt)
	return r.view
}

func renderViewportFull(
	s *Session,
	conversation map[string][]Turn,
	w, h, spinnerFrame, scrollOffset, newCount int,
	active bool,
	usage sessionUsage,
	thinkingStartedAt time.Time,
) viewportRender {
	borderColor := inactivePaneBorderColor
	if active {
		borderColor = activePaneBorderColor
	}
	if s == nil {
		dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		bold := lipgloss.NewStyle().Bold(true)
		body := bold.Render("no sessions yet") + "\n\n" +
			dim.Render("press") + " " +
			bold.Render("Ctrl+N") + " " +
			dim.Render("to create one") + "\n" +
			dim.Render("or") + " " +
			bold.Render("chubby spawn --name <n> --cwd <dir>") + " " +
			dim.Render("from another terminal")
		return viewportRender{
			view: lipgloss.NewStyle().Width(w).Height(h).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(borderColor).
				Padding(1, 2).
				Render(body),
		}
	}
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(w).Height(h)
	isThinking := s.Status == "thinking"
	header := renderSessionBanner(s, spinnerFrame, scrollOffset > 0,
		usage, thinkingStartedAt, isThinking)
	// Banner may now span two lines (activity row). Count its actual
	// rendered height so visibleH below subtracts the right amount.
	bannerLines := strings.Count(header, "\n") + 1
	if s.Status == "dead" {
		hint := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true).
			Render("session is dead — press Ctrl+P to respawn")
		body := header + "\n\n" + hint
		turns := conversation[s.ID]
		if len(turns) > 0 {
			// Still show the prior transcript below the hint so the user
			// can see what happened before the session died.
			body += "\n\n" + renderTurns(turns, s.Color, w-2)
		}
		return viewportRender{view: frame.Render(body)}
	}
	turns := conversation[s.ID]
	if len(turns) == 0 {
		body := header + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
				Render("(no messages yet — type below to send)")
		return viewportRender{view: frame.Render(body)}
	}
	turnsBody := renderTurns(turns, s.Color, w-2)
	// Slice the rendered transcript by scrollOffset. The line count
	// we report excludes the banner+spacer, so callers can clamp
	// scrollOffset against just the content.
	lines := strings.Split(strings.TrimRight(turnsBody, "\n"), "\n")
	lineCount := len(lines)
	// Visible body area: total inner height minus banner + spacer.
	// (-2 for the rounded border was already applied via frame.Width.)
	// banner height is dynamic now (1 row when no usage / not thinking,
	// 2 rows when the activity line is present).
	visibleH := h - 2 - bannerLines - 1 // border (top+bottom) + banner rows + spacer
	if visibleH < 1 {
		visibleH = 1
	}
	end := lineCount - scrollOffset
	if end < 0 {
		end = 0
	}
	if end > lineCount {
		end = lineCount
	}
	start := end - visibleH
	if start < 0 {
		start = 0
	}
	visible := strings.Join(lines[start:end], "\n")
	body := header + "\n\n" + visible

	// "↓ N new" badge — only when actually scrolled away from the
	// bottom AND there are new turns the user hasn't seen yet.
	if scrollOffset > 0 && newCount > 0 {
		badge := lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")).Bold(true).
			Render(fmt.Sprintf("↓ %d new · End to jump", newCount))
		// Right-align the badge inside the inner width.
		body += "\n" + lipgloss.NewStyle().Width(w-2).
			Align(lipgloss.Right).Render(badge)
	}
	return viewportRender{view: frame.Render(body), lineCount: lineCount}
}

// renderTurns formats the structured transcript: user prompts marked
// with a coloured arrow, assistant responses in the default fg,
// separated by blank lines. Pass innerWidth to fit inside a bordered
// frame (subtract 2 for the rounded border).
//
// File-path mentions get a cyan-underline accent so the user can spot
// the things Ctrl+] would jump to.
func renderTurns(turns []Turn, sessionColor string, innerWidth int) string {
	if innerWidth < 10 {
		innerWidth = 10
	}
	userStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(sessionColor)).Bold(true).
		Width(innerWidth)
	asstStyle := lipgloss.NewStyle().Width(innerWidth)
	var b strings.Builder
	for i, t := range turns {
		if i > 0 {
			b.WriteString("\n")
		}
		switch t.Role {
		case "user":
			b.WriteString(userStyle.Render("▸ " + stylePathMentions(t.Text)))
		default:
			b.WriteString(asstStyle.Render(stylePathMentions(t.Text)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// pathStyle highlights inline path mentions: cyan + underline so the
// reader can spot a Ctrl+] target without it overpowering the prose.
var pathStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("14")).
	Underline(true)

// stylePathMentions wraps every regex-matched path in pathStyle. We
// run this on the plain text before lipgloss layout so the styling
// composes with the user/assistant outer style. Non-matching segments
// pass through unchanged.
func stylePathMentions(s string) string {
	idx := pathRE.FindAllStringIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, ix := range idx {
		b.WriteString(s[last:ix[0]])
		b.WriteString(pathStyle.Render(s[ix[0]:ix[1]]))
		last = ix[1]
	}
	b.WriteString(s[last:])
	return b.String()
}
