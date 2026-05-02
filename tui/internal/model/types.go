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
//
// ClaudeSessionID is the JSONL transcript id (the value `claude
// --resume <id>` takes). May be empty when the wrapper just
// started — claude binds the JSONL on first use, the daemon's
// transcript watcher fills the field via session_id_resolved.
type Session struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Color           string        `json:"color"`
	Kind            SessionKind   `json:"kind"`
	Status          SessionStatus `json:"status"`
	Cwd             string        `json:"cwd"`
	Tags            []string      `json:"tags"`
	ClaudeSessionID string        `json:"claude_session_id"`
}

// EventMethod identifies the daemon-side broadcast events the TUI
// subscribes to. These match chubbyd's broadcast() topic strings —
// kept in sync by convention because both encode against JSON. Pulled
// out as named constants so the giant Update() switch can lean on
// `case EventTranscriptMessage:` instead of bare string literals
// (which silently miss on typo).
type EventMethod string

const (
	// EventTranscriptMessage carries one user/assistant turn from the
	// JSONL tailer. Params: session_id, role, text, tool_calls, ts.
	EventTranscriptMessage EventMethod = "transcript_message"
	// EventToolResult splices a tool_result preview onto a previously-
	// broadcast tool_call. Params: session_id, tool_use_id, preview,
	// is_error, ts.
	EventToolResult EventMethod = "tool_result"
	// EventSessionUsageChanged updates token totals for the banner.
	// Params: session_id, input_tokens, output_tokens,
	// cache_read_input_tokens, ts.
	EventSessionUsageChanged EventMethod = "session_usage_changed"
	// EventSessionStatusChanged fires on every status flip. Params:
	// session_id (or `id`), status.
	EventSessionStatusChanged EventMethod = "session_status_changed"
	// EventSessionAdded / Renamed / Recolored / Tagged trigger a list
	// refresh — the rail is rebuilt from scratch from the new snapshot.
	EventSessionAdded    EventMethod = "session_added"
	EventSessionRenamed  EventMethod = "session_renamed"
	EventSessionRecolored EventMethod = "session_recolored"
	EventSessionTagged   EventMethod = "session_tagged"
	// EventSessionIDResolved fires when the daemon binds a JSONL
	// transcript to a previously-unbound session — TUI should re-fetch
	// history because earlier loadHistory returned empty.
	EventSessionIDResolved EventMethod = "session_id_resolved"
	// EventPtyChunk carries a base64-encoded slice of raw PTY bytes
	// for one session. The TUI feeds the bytes into its per-session
	// vt emulator (m.pty[sid]) so the conversation pane shows
	// claude's UI live.
	EventPtyChunk EventMethod = "pty_chunk"
)
