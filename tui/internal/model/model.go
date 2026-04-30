// Package model is the Bubble Tea Model for chub-tui: the top-level
// session-list rail, focused-viewport pane, compose bar, and event/refresh
// wiring. Modal panes (broadcast, grep, history) are layered on top and
// share Mode dispatch.
package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/USER/chub/tui/internal/rpc"
	"github.com/USER/chub/tui/internal/views"
)

// Session mirrors the SessionDict schema returned by chubd's list_sessions.
type Session struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Color  string   `json:"color"`
	Kind   string   `json:"kind"`
	Status string   `json:"status"`
	Cwd    string   `json:"cwd"`
	Tags   []string `json:"tags"`
}

// outputCap is the rolling per-session live buffer size in bytes.
const outputCap = 64 * 1024

// Mode controls which modal pane is on top of the main two-pane layout.
// Subsequent phases add ModeGrep, ModeHistory, ModeReconnecting.
type Mode int

const (
	ModeMain Mode = iota
	ModeBroadcast
	ModeGrep
)

// broadcastState is held in the Model so the reducer can mutate it
// cleanly. Tab cycles fields {0=list,1=text,2=send}; space toggles a
// session checkbox; Enter on Send fires inject for each selected id.
type broadcastState struct {
	selected map[string]bool
	field    int
	cursor   int
	text     string
}

// grepState backs the FTS palette: a textinput plus a debounced
// search command pipeline. debounceToken invalidates stale results.
type grepState struct {
	query         textinput.Model
	results       []map[string]any
	cursor        int
	debounceToken int
}

// Model is the Bubble Tea state.
type Model struct {
	client   *rpc.Client
	sessions []Session
	focused  int
	output   map[string]string
	width    int
	height   int
	err      error

	mode    Mode
	compose textinput.Model

	bcast broadcastState
	grep  grepState
}

type tickMsg struct{}
type evMsg rpc.Event
type listMsg []Session
type errMsg struct{ err error }
type composeSentMsg struct{}
type composeFailedMsg struct{ err error }
type bcastDoneMsg struct{ n int }
type grepDebounceMsg struct{ token int }
type grepResultsMsg struct {
	token   int
	matches []map[string]any
}

// New constructs a Model bound to an already-connected rpc.Client.
func New(c *rpc.Client) Model {
	return Model{
		client:  c,
		output:  map[string]string{},
		mode:    ModeMain,
		compose: views.NewCompose(),
		bcast:   broadcastState{selected: map[string]bool{}},
		grep:    grepState{query: views.NewGrepQuery()},
	}
}

// Init kicks off initial session refresh, event listening, and tick loop.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshSessions(), m.listenEvents(), tickEvery())
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

func (m Model) listenEvents() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ev, ok := <-c.Events()
		if !ok {
			return nil
		}
		return evMsg(ev)
	}
}

func tickEvery() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
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
		return m, m.listenEvents()
	case evMsg:
		ev := rpc.Event(msg)
		if ev.Method == "event" {
			subM, _ := ev.Params["event_method"].(string)
			subP, _ := ev.Params["event_params"].(map[string]any)
			switch subM {
			case "output_chunk":
				sid, _ := subP["session_id"].(string)
				b64, _ := subP["data_b64"].(string)
				if data, err := base64.StdEncoding.DecodeString(b64); err == nil {
					cur := m.output[sid] + string(data)
					if len(cur) > outputCap {
						cur = cur[len(cur)-outputCap:]
					}
					m.output[sid] = cur
				}
			case "session_added", "session_renamed",
				"session_recolored", "session_status_changed",
				"session_tagged":
				return m, tea.Batch(m.refreshSessions(), m.listenEvents())
			}
		}
		return m, m.listenEvents()
	case tickMsg:
		return m, tea.Batch(m.refreshSessions(), tickEvery())
	case errMsg:
		m.err = msg.err
		return m, nil
	case composeSentMsg:
		m.compose.SetValue("")
		return m, nil
	case composeFailedMsg:
		m.err = msg.err
		return m, nil
	case bcastDoneMsg:
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
	default:
		return m.handleKeyMain(msg)
	}
}

func (m Model) handleKeyMain(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		if len(m.sessions) > 0 {
			m.focused = (m.focused + 1) % len(m.sessions)
		}
		return m, nil
	case "shift+tab":
		if len(m.sessions) > 0 {
			m.focused = (m.focused - 1 + len(m.sessions)) % len(m.sessions)
		}
		return m, nil
	case "ctrl+b":
		m.mode = ModeBroadcast
		m.bcast = broadcastState{selected: map[string]bool{}}
		return m, nil
	case "/":
		// "/" enters grep palette only when the compose bar is empty,
		// so a literal slash typed mid-prompt still goes to the buffer.
		if m.compose.Value() == "" {
			m.mode = ModeGrep
			m.grep.query = views.NewGrepQuery()
			m.grep.results = nil
			m.grep.cursor = 0
			return m, nil
		}
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
	return m, cmd
}

func (m Model) handleKeyBroadcast(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeMain
		return m, nil
	case "tab":
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
		}
	case 1: // textarea
		switch msg.String() {
		case "enter":
			m.bcast.text += "\n"
		case "backspace":
			if len(m.bcast.text) > 0 {
				m.bcast.text = m.bcast.text[:len(m.bcast.text)-1]
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.bcast.text += string(msg.Runes)
			} else if msg.String() == " " {
				m.bcast.text += " "
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
func (m Model) sendComposed() tea.Cmd {
	text := m.compose.Value()
	if text == "" {
		return nil
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

// View renders the dual-pane layout plus the compose bar, or the
// active modal pane when m.mode != ModeMain.
func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v\npress ctrl+c to quit", m.err)
	}
	switch m.mode {
	case ModeBroadcast:
		return m.viewBroadcast()
	case ModeGrep:
		return m.viewGrep()
	}
	leftW := 24
	rightW := m.width - leftW - 2
	if rightW < 20 {
		rightW = 20
	}
	composeH := 3
	h := m.height - composeH - 2
	if h < 5 {
		h = 5
	}
	left := renderList(m.sessions, m.focused, leftW, h)
	right := renderViewport(m.focusedSession(), m.output, rightW, h)
	main := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	target := "(no session)"
	color := "#888"
	if s := m.focusedSession(); s != nil {
		target = "@" + s.Name
		color = s.Color
	}
	composeBar := views.RenderCompose(m.compose, target, color, m.width)
	return lipgloss.JoinVertical(lipgloss.Left, main, composeBar)
}

func (m Model) viewBroadcast() string {
	w := m.width
	if w < 40 {
		w = 40
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(
		"Broadcast (Tab=switch field, Space=toggle, Esc=cancel)") + "\n\n")

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
	b.WriteString(textStyle.Render(m.bcast.text+"_") + "\n")

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

func (m Model) focusedSession() *Session {
	if m.focused < 0 || m.focused >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.focused]
}

var statusGlyph = map[string]string{
	"idle":          "○",
	"thinking":      "●",
	"awaiting_user": "⚡",
	"dead":          "✕",
}

func renderList(ss []Session, focused, w, h int) string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(" Sessions") + "\n")
	for i, s := range ss {
		marker := "  "
		if i == focused {
			marker = "▣ "
		}
		col := lipgloss.Color(s.Color)
		glyph := statusGlyph[s.Status]
		if glyph == "" {
			glyph = "·"
		}
		line := fmt.Sprintf("%s%s %s", marker,
			lipgloss.NewStyle().Foreground(col).Render(s.Name),
			glyph)
		b.WriteString(lipgloss.NewStyle().Width(w).Render(line) + "\n")
	}
	return lipgloss.NewStyle().Width(w).Height(h).Border(lipgloss.RoundedBorder()).Render(b.String())
}

func renderViewport(s *Session, output map[string]string, w, h int) string {
	if s == nil {
		return lipgloss.NewStyle().Width(w).Height(h).Border(lipgloss.RoundedBorder()).
			Render("(no session)")
	}
	body := output[s.ID]
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color(s.Color)).
		Border(lipgloss.RoundedBorder()).
		Width(w).Height(h)
	return style.Render(s.Name + "\n\n" + body)
}
