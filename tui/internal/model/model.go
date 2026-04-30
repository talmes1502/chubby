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
// Subsequent phases add ModeBroadcast, ModeGrep, ModeHistory, ModeReconnecting.
type Mode int

const (
	ModeMain Mode = iota
)

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
}

type tickMsg struct{}
type evMsg rpc.Event
type listMsg []Session
type errMsg struct{ err error }
type composeSentMsg struct{}
type composeFailedMsg struct{ err error }

// New constructs a Model bound to an already-connected rpc.Client.
func New(c *rpc.Client) Model {
	return Model{
		client:  c,
		output:  map[string]string{},
		mode:    ModeMain,
		compose: views.NewCompose(),
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
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
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

// View renders the dual-pane layout plus the compose bar.
func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v\npress ctrl+c to quit", m.err)
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
