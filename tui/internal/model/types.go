// Package model — types.go: domain types shared across the model
// package. Loose-string fields (Status, Kind, Role) are typed-string
// enums so a typo is a compile-time error instead of a silently-failing
// equality check. JSON serialization is byte-identical to plain
// strings, so the wire format with the daemon stays the same.
package model

// SessionStatus is the current state of a session. Mirrors the daemon's
// chubby.daemon.session.SessionStatus enum; values must stay in sync
// with that side because both encode/decode against the same JSON.
type SessionStatus string

const (
	// StatusIdle is the default — the session is alive but no inject is
	// in flight. The status glyph is the open circle "○".
	StatusIdle SessionStatus = "idle"
	// StatusThinking — an inject was just made and the assistant hasn't
	// finished its response yet. Drives the rail spinner and the
	// "✢ Thinking… / Generating…" banner.
	StatusThinking SessionStatus = "thinking"
	// StatusAwaitingUser — Claude finished a response (Stop hook fired)
	// and is parked waiting for the user's next prompt. Banner glyph: ⚡
	StatusAwaitingUser SessionStatus = "awaiting_user"
	// StatusDead — the wrapper exited or the session was released. The
	// rail keeps showing the row (with ✕ glyph) so users can respawn it
	// via Ctrl+P; list operations skip dead sessions when allocating
	// names/colors.
	StatusDead SessionStatus = "dead"
)

// SessionKind matches the daemon-side enum: how the session is being
// reached. Wrapped sessions are spawned by chubbyd and routed through
// chubby-claude; readonly is an externally-running claude that chubby
// only observes via JSONL tailing; spawned/tmux are variations the
// daemon supports for attach flows.
type SessionKind string

const (
	KindWrapped       SessionKind = "wrapped"
	KindReadonly      SessionKind = "readonly"
	KindSpawned       SessionKind = "spawned"
	KindTmuxAttached  SessionKind = "tmux_attached"
)

// TurnRole identifies who produced a transcript turn. The wire field
// uses lowercase strings to match Claude's JSONL format.
type TurnRole string

const (
	RoleUser      TurnRole = "user"
	RoleAssistant TurnRole = "assistant"
)

// Turn is a single round in the conversation: either a user prompt or
// the assistant's response. Text is the prose body only. Tool calls
// (Bash, Edit, Read, …) live in Tools as structured records so the
// viewport can render each one as a styled box matching Claude Code's
// UI.
type Turn struct {
	Role  TurnRole
	Text  string
	Tools []ToolCall
	Ts    int64
}

// ToolCall describes a single tool invocation made by the model during
// an assistant turn. ID matches Claude's tool_use_id so the tailer can
// splice in ResultPreview when the matching tool_result arrives in a
// follow-up record.
type ToolCall struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Summary       string `json:"summary"`
	ResultPreview string `json:"result_preview"`
	ResultIsError bool   `json:"result_is_error"`
}

// Session mirrors the SessionDict schema returned by chubbyd's
// list_sessions RPC. Status and Kind are typed-string enums so
// equality checks are typo-safe at compile time.
type Session struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Color  string        `json:"color"`
	Kind   SessionKind   `json:"kind"`
	Status SessionStatus `json:"status"`
	Cwd    string        `json:"cwd"`
	Tags   []string      `json:"tags"`
}
