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
)

// renameTarget says whether ModeRename is editing a session name or a
// group label (the first tag across a set of sessions).
type renameTarget int

const (
	RenameSession renameTarget = iota
	RenameGroup
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
// (name + cwd + group). Tab cycles between the fields. cwd defaults to
// the focused session's cwd or $HOME and supports ~ expansion at
// submit. group is optional; when non-empty, it's passed as the first
// tag so the new session lands in that rail group (GroupKey gives
// precedence to the first tag).
type spawnState struct {
	name  textinput.Model
	cwd   textinput.Model
	group textinput.Model
	field int // 0=name, 1=cwd, 2=group
	err   error
}

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

	bcast   broadcastState
	grep    grepState
	history historyState
	spawn   spawnState
	search  searchState
	rename  renameState

	toasts []toast

	reconnectAttempts int

	// groupCollapsed tracks which group headers are collapsed in the
	// left rail. Persisted to ~/.claude/hub/tui-state.json on change.
	groupCollapsed map[string]bool
	// railCollapsed hides the left rail entirely; viewport takes full
	// width. Toggled with Ctrl+J. Persisted in tui-state.json.
	railCollapsed bool
	// railCursor indexes the currently-highlighted row in the visible
	// rail (may be a group header or a session). Up/Down walks this;
	// Tab/Shift+Tab walks m.focused (session-only).
	railCursor int

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
type renameDoneMsg struct{}
type respawnDoneMsg struct{}
type historyTurnsMsg struct {
	sid   string
	turns []Turn
}
type copiedMsg struct{ count int }

// autoSpawnedMsg is emitted when the empty-startup auto-spawn succeeds.
// The reducer surfaces a transient toast and triggers a session refresh.
type autoSpawnedMsg struct{ name, cwd string }

// autoSpawnFallbackMsg is emitted when auto-spawn cannot reasonably
// succeed (HOME unresolvable, every "temp"/"temp-N" already taken,
// non-name-collision RPC error). The reducer falls back to opening the
// spawn modal so the user can pick something else.
type autoSpawnFallbackMsg struct{ err error }

// New constructs a Model bound to an already-connected rpc.Client.
func New(c *rpc.Client) Model {
	return Model{
		client:         c,
		conversation:   map[string][]Turn{},
		mode:           ModeMain,
		compose:        views.NewCompose(),
		bcast:          broadcastState{selected: map[string]bool{}},
		grep:           grepState{query: views.NewGrepQuery()},
		groupCollapsed: LoadCollapsedGroups(),
		railCollapsed:  LoadRailCollapsed(),
		search:         searchState{query: views.NewSearchQuery()},
		historyLoaded:  map[string]bool{},
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
		return m, nil
	case listMsg:
		m.sessions = []Session(msg)
		if m.focused >= len(m.sessions) {
			m.focused = 0
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
				turns := append(m.conversation[sid], Turn{
					Role: role,
					Text: text,
					Ts:   int64(ts),
				})
				if len(turns) > turnsCap {
					turns = turns[len(turns)-turnsCap:]
				}
				m.conversation[sid] = turns
			case "session_status_changed":
				sid, _ := subP["session_id"].(string)
				newStatus, _ := subP["status"].(string)
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
		// Chub-side commands (/color, /rename, /tag, /refresh-claude)
		// need a refresh so the new color/name/tags propagate to the
		// rail immediately. Any non-empty toast is surfaced via the same
		// transient message bubble we use for awaiting_user notifications.
		m.compose.SetValue("")
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
	case spawnFailedMsg:
		m.spawn.err = msg.err
		return m, nil
	case renameDoneMsg:
		m.mode = ModeMain
		return m, m.refreshSessions()
	case respawnDoneMsg:
		return m, m.refreshSessions()
	case historyTurnsMsg:
		// Replace any partially-live conversation with the loaded history.
		// If live events arrived during the load, they're discarded — the
		// JSONL is the canonical source. New live events after this will
		// append normally.
		m.conversation[msg.sid] = msg.turns
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
	case ModeSearch:
		return m.handleKeySearch(msg)
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
		m.cycleFocusedSession(+1)
		return m, nil
	case "shift+tab":
		m.cycleFocusedSession(-1)
		return m, nil
	case "up", "k":
		// Compose forwarding fallthrough is below; only intercept these
		// when the compose bar is empty so the user can still type 'k'.
		if m.compose.Value() == "" {
			m.moveRailCursor(-1)
			return m, nil
		}
	case "down", "j":
		if m.compose.Value() == "" {
			m.moveRailCursor(+1)
			return m, nil
		}
	case " ":
		// Space toggles a group header collapse, but only when compose
		// is empty (otherwise space goes to the textinput).
		if m.compose.Value() == "" {
			rows := m.railRows()
			if m.railCursor >= 0 && m.railCursor < len(rows) {
				r := rows[m.railCursor]
				if r.Kind == RailRowHeader {
					m.groupCollapsed[r.GroupName] = !m.groupCollapsed[r.GroupName]
					_ = SaveTUIState(TUIState{
						GroupsCollapsed: collapsedGroupNames(m.groupCollapsed),
						RailCollapsed:   m.railCollapsed,
					})
					return m, nil
				}
			}
		}
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
	case "enter":
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
		m.spawn.field = (m.spawn.field + 1) % 3
		m.refocusSpawn()
		return m, nil
	case "shift+tab":
		m.spawn.field = (m.spawn.field + 2) % 3
		m.refocusSpawn()
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.spawn.name.Value())
		if name == "" {
			m.spawn.field = 0
			m.refocusSpawn()
			return m, nil
		}
		cwd := views.ExpandHome(strings.TrimSpace(m.spawn.cwd.Value()))
		group := strings.TrimSpace(m.spawn.group.Value())
		var tags []string
		if group != "" {
			tags = []string{group}
		}
		return m, m.doSpawn(name, cwd, tags)
	}
	var cmd tea.Cmd
	switch m.spawn.field {
	case 0:
		m.spawn.name, cmd = m.spawn.name.Update(msg)
	case 1:
		m.spawn.cwd, cmd = m.spawn.cwd.Update(msg)
	case 2:
		m.spawn.group, cmd = m.spawn.group.Update(msg)
	}
	return m, cmd
}

// openSpawnModal seeds spawnState (cwd defaults to focused session's cwd
// or $HOME, group defaults to focused session's group when meaningful)
// and switches to ModeSpawn. Pointer receiver because we mutate m.mode
// and m.spawn. Called by Ctrl+N and by the auto-open path in listMsg
// when the first list comes back empty.
func (m *Model) openSpawnModal() {
	cwd := ""
	group := ""
	if s := m.focusedSession(); s != nil {
		cwd = s.Cwd
		// Pre-fill the group with the focused session's group so the
		// new session lands in the same rail bucket. Skip "(untitled)"
		// — that's the no-group fallback, not a meaningful tag.
		if g := GroupKey(*s); g != UntitledGroup {
			group = g
		}
	}
	if cwd == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cwd = home
		}
	}
	m.spawn = spawnState{
		name:  views.NewSpawnNameInput(),
		cwd:   views.NewSpawnCwdInput(cwd),
		group: views.NewSpawnGroupInput(group),
		field: 0,
	}
	m.mode = ModeSpawn
}

// refocusSpawn applies Focus()/Blur() so only the active spawn-modal
// field shows the cursor. Called whenever m.spawn.field changes.
func (m *Model) refocusSpawn() {
	m.spawn.name.Blur()
	m.spawn.cwd.Blur()
	m.spawn.group.Blur()
	switch m.spawn.field {
	case 0:
		m.spawn.name.Focus()
	case 1:
		m.spawn.cwd.Focus()
	case 2:
		m.spawn.group.Focus()
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

func (m Model) doSpawn(name, cwd string, tags []string) tea.Cmd {
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
		if _, err := c.Call(context.Background(), "spawn_session", params); err != nil {
			return spawnFailedMsg{err}
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
	case RailRowHeader:
		ids := []string{}
		for _, s := range m.sessions {
			if GroupKey(s) == row.GroupName {
				ids = append(ids, s.ID)
			}
		}
		input.SetValue(row.GroupName)
		input.CursorEnd()
		input.Focus()
		m.rename = renameState{
			input:    input,
			target:   RenameGroup,
			sessions: ids,
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
		if m.rename.target == RenameSession {
			return m, m.doRenameSession(m.rename.sessions[0], newName)
		}
		// RenameGroup: bulk retag all sessions in the group.
		return m, m.doRenameGroup(m.rename.sessions, m.rename.oldName, newName)
	}
	var cmd tea.Cmd
	m.rename.input, cmd = m.rename.input.Update(msg)
	return m, cmd
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
	for _, head := range []string{"/refresh-claude", "/color", "/rename", "/tag"} {
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
	case ModeHelp:
		return m.wrapWithChrome(m.viewHelp())
	case ModeReconnecting:
		return m.wrapWithChrome(m.viewReconnecting())
	case ModeSearch:
		// Falls through to the main layout below; the rail renderer
		// adds the search bar based on m.mode == ModeSearch.
	}
	leftW := 24
	rightW := m.width - leftW - 2
	if rightW < 20 {
		rightW = 20
	}
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
	var main string
	if m.railCollapsed {
		// Single full-width pane: viewport spans the entire terminal
		// (minus the small lipgloss frame allowance JoinVertical adds).
		fullW := m.width - 2
		if fullW < 20 {
			fullW = 20
		}
		main = renderViewport(m.focusedSession(), m.conversation, fullW, h, m.spinnerFrame)
	} else {
		rows := m.railRows()
		focusedID := ""
		if s := m.focusedSession(); s != nil {
			focusedID = s.ID
		}
		searchHeader := ""
		if m.mode == ModeSearch {
			searchHeader = m.search.query.View()
		} else if q := m.search.query.Value(); q != "" {
			searchHeader = "/ " + q
		}
		left := renderList(rows, m.railCursor, focusedID, m.groupCollapsed, searchHeader, leftW, h, m.spinnerFrame)
		right := renderViewport(m.focusedSession(), m.conversation, rightW, h, m.spinnerFrame)
		main = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}

	target := "(no session)"
	color := "#888"
	if s := m.focusedSession(); s != nil {
		target = "@" + s.Name
		color = s.Color
	}
	composeBar := views.RenderCompose(m.compose, target, color, m.composeGhost(), m.width)
	body := lipgloss.JoinVertical(lipgloss.Left, main, composeBar)
	if m.slashPopupVisible() {
		popup := views.RenderSlashPopup(m.slashPopupCmds, m.slashPopupCursor, m.width)
		body = lipgloss.JoinVertical(lipgloss.Left, body, popup)
	}
	return m.overlayToasts(m.wrapWithChrome(body))
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

  Tab / Shift+Tab    cycle focused session
  Up / Down k / j    walk session list
  Space              toggle group collapse
  Enter              send composed message to focused session
  @name <msg>        one-shot redirect: send to <name>, then snap back
  Tab (in compose)   autocomplete @name or /command
  /<name>            Claude slash command (Tab completes; e.g. /model sonnet)
  /color #RRGGBB     (chubby) recolor focused session
  /rename <name>     (chubby) rename focused session
  /tag +foo -bar     (chubby) modify tags
  /refresh-claude    (chubby) restart claude with --resume (picks up settings changes)

  Ctrl+N             new session in focused cwd
  Ctrl+K             search session list
  Ctrl+R             rename focused session OR group (rail cursor)
  Ctrl+P             respawn focused dead session
  Ctrl+B             broadcast modal
  Ctrl+G             grep transcripts (current run)
  Ctrl+H             history panel (past hub-runs)
  Ctrl+J             toggle rail (full-width conversation)
  Ctrl+Y             copy focused session's conversation to clipboard

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
	b.WriteString(label("name:  ", m.spawn.field == 0) + m.spawn.name.View() + "\n")
	b.WriteString(label("cwd:   ", m.spawn.field == 1) + m.spawn.cwd.View() + "\n")
	b.WriteString(label("group: ", m.spawn.field == 2) + m.spawn.group.View() + "\n\n")
	if m.spawn.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Render("error: "+m.spawn.err.Error()) + "\n\n")
	}
	b.WriteString(dim.Render("Tab to switch field · Enter to spawn · Esc to cancel"))
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
	if m.rename.target == RenameGroup {
		title = "Rename group"
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
	return BuildRailRows(visible, m.sessions, m.groupCollapsed)
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

// moveRailCursor walks all rail rows by `dir` (+1 or -1). When the
// cursor lands on a session, m.focused is updated; landing on a header
// leaves the focused session alone.
func (m *Model) moveRailCursor(dir int) {
	rows := m.railRows()
	if len(rows) == 0 {
		return
	}
	m.railCursor = (m.railCursor + dir + len(rows)) % len(rows)
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

// renderList draws the grouped left rail. rows is the flattened
// header+session list from BuildRailRows; cursor is the highlighted
// row; focusedID is the currently-focused session's ID (gets the ▣
// marker even if it's not the cursor row); searchHeader (optional) is
// rendered just below the "Sessions" title so the user sees the active
// filter.
func renderList(rows []RailRow, cursor int, focusedID string, collapsed map[string]bool, searchHeader string, w, h, spinnerFrame int) string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(" Sessions") + "\n")
	if searchHeader != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("12")).
			Render(" "+searchHeader) + "\n")
	}
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	for i, r := range rows {
		switch r.Kind {
		case RailRowHeader:
			arrow := "▾"
			if collapsed[r.GroupName] {
				arrow = "▸"
			}
			cursorMark := " "
			if i == cursor {
				cursorMark = ">"
			}
			line := fmt.Sprintf("%s %s %s", cursorMark, arrow, r.GroupName)
			b.WriteString(headerStyle.Render(line) + "\n")
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
	return lipgloss.NewStyle().Width(w).Height(h).Border(lipgloss.RoundedBorder()).Render(b.String())
}

// renderSessionBanner builds the colored top-of-viewport header:
//
//	┃ <name>  ●  <cwd> · <kind> · <status-glyph> <status>
//
// The bar (U+2503) and session-name are rendered in the session's color
// (bold). The dot (U+25CF) acts as a swatch, also in the session's color.
// cwd / kind / status are dimmed so they read as metadata rather than
// content.
func renderSessionBanner(s *Session, spinnerFrame int) string {
	col := lipgloss.Color(s.Color)
	colorStyle := lipgloss.NewStyle().Foreground(col).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	glyph := statusGlyph(s.Status, spinnerFrame)
	bar := colorStyle.Render("┃")
	name := colorStyle.Render(s.Name)
	swatch := lipgloss.NewStyle().Foreground(col).Render("●")
	// dim styles the metadata segment, but the glyph itself is already
	// styled by statusGlyph (e.g. bright yellow for the spinner) — we
	// keep it outside the dim wrapper so the spinner stays vivid.
	meta := dim.Render(fmt.Sprintf("%s · %s · ", s.Cwd, s.Kind)) + glyph + dim.Render(" "+s.Status)
	return fmt.Sprintf("%s %s  %s  %s", bar, name, swatch, meta)
}

// renderViewport draws the focused session's structured conversation:
// a colored session banner, then user prompts marked with a coloured
// arrow and assistant responses in the default fg, separated by blank
// lines. The previous implementation rendered the raw PTY byte stream,
// which was unreadable inside lipgloss because Claude's cursor-
// positioning escapes don't compose with lipgloss frames.
func renderViewport(s *Session, conversation map[string][]Turn, w, h, spinnerFrame int) string {
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
		return lipgloss.NewStyle().Width(w).Height(h).
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			Render(body)
	}
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(w).Height(h)
	header := renderSessionBanner(s, spinnerFrame)
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
		return frame.Render(body)
	}
	turns := conversation[s.ID]
	if len(turns) == 0 {
		body := header + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
				Render("(no messages yet — type below to send)")
		return frame.Render(body)
	}
	body := header + "\n\n" + renderTurns(turns, s.Color, w-2)
	return frame.Render(body)
}

// renderTurns formats the structured transcript: user prompts marked
// with a coloured arrow, assistant responses in the default fg,
// separated by blank lines. Pass innerWidth to fit inside a bordered
// frame (subtract 2 for the rounded border).
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
			b.WriteString(userStyle.Render("▸ " + t.Text))
		default:
			b.WriteString(asstStyle.Render(t.Text))
		}
		b.WriteString("\n")
	}
	return b.String()
}
