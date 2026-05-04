"""Pydantic models for every RPC's params and result. New RPCs append here."""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, ConfigDict


class _Strict(BaseModel):
    model_config = ConfigDict(extra="forbid")


# --- ping / version (Phase 2) --------------------------------------------------


class PingParams(_Strict):
    message: str | None = None


class PingResult(_Strict):
    echo: str | None = None
    server_time_ms: int


class VersionParams(_Strict):
    pass


class VersionResult(_Strict):
    version: str
    protocol: int


# --- session registry (Phase 3) ------------------------------------------------


class RegisterWrappedParams(_Strict):
    name: str
    cwd: str
    pid: int
    tags: list[str] = []
    color: str | None = None
    claude_pid: int | None = None


class SessionDict(_Strict):
    id: str
    hub_run_id: str
    name: str
    color: str
    kind: str
    cwd: str
    created_at: int
    last_activity_at: int
    status: str
    pid: int | None = None
    claude_session_id: str | None = None
    tmux_target: str | None = None
    tags: list[str] = []
    ended_at: int | None = None
    # Transient git state populated by the daemon's git-status sweep;
    # not persisted to the SQLite store. ``None`` means "no upstream
    # configured / not a git repo / not yet polled" — TUI hides the
    # glyph in that case.
    git_ahead: int | None = None
    git_behind: int | None = None
    # Set when the session was spawned with ``--branch``/``--pr`` and
    # the daemon owns a chubby-managed git worktree at this path.
    # Used so cleanup can find the worktree on release/detach. Also
    # surfaces to the TUI for "branch X" labels in the rail.
    worktree_path: str | None = None
    # Transient list of detected listening ports under this session's
    # process tree, populated by the daemon's periodic port sweep.
    # Each entry: {port: int, pid: int, address: str}. Surfaces to
    # the rail as "🌐 :3000".
    ports: list[dict[str, Any]] = []
    # Cached first user-turn from the JSONL transcript (Phase 8c).
    # Populated once by the watch_for_transcript hook; surfaces in the
    # quick-switcher rows + rail tooltip. ``None`` = not yet bound.
    first_user_message: str | None = None


class RegisterWrappedResult(_Strict):
    session: SessionDict


class ListSessionsParams(_Strict):
    kind: str | None = None


class ListSessionsResult(_Strict):
    sessions: list[SessionDict]


class RenameSessionParams(_Strict):
    id: str
    name: str


class RecolorSessionParams(_Strict):
    id: str
    color: str


# --- wrapper output + injection (Phase 6) -------------------------------------


class PushOutputParams(_Strict):
    session_id: str
    seq: int
    data_b64: str  # base64-encoded raw bytes
    role: str = "raw"  # raw | user | assistant | tool


class InjectParams(_Strict):
    session_id: str
    payload_b64: str


class ResizePtyParams(_Strict):
    session_id: str
    rows: int
    cols: int


class GetPtyBufferParams(_Strict):
    session_id: str


class GetPtyBufferResult(_Strict):
    buffer_b64: str


class SpawnSessionParams(_Strict):
    name: str
    # Required and non-empty. Sessions are organized around a project
    # cwd (rail grouping, JSONL location, hooks scope) so a "no project"
    # session has no consistent meaning. The TUI spawn modal pre-fills
    # cwd from the focused session or $HOME, and the CLI defaults to
    # ``os.getcwd()`` — so users almost never have to type it. The
    # daemon rejects an empty cwd to surface sloppy scripted invocations
    # rather than silently defaulting.
    cwd: str
    tags: list[str] = []
    # Optional branch/PR mode — when set, spawn_session resolves a git
    # worktree at ``~/.claude/chubby/worktrees/<repo-hash>/<branch>``
    # and overrides ``cwd`` with that path. Three modes by argument
    # shape:
    #   - ``branch`` only, branch doesn't exist → create new from HEAD.
    #   - ``branch`` only, branch exists → check out into a fresh
    #     worktree (no -b flag).
    #   - ``pr`` set → resolve PR head ref via ``gh pr view`` and use
    #     that branch (best-effort; falls back to existing-branch mode
    #     when ``gh`` isn't authenticated).
    branch: str | None = None
    pr: int | None = None
    # Phase 8d cross-project history browser: when set, the wrapper
    # adds ``--resume <id>`` to claude's argv on the first iteration
    # so the new session re-opens an existing JSONL transcript.
    resume_claude_session_id: str | None = None


# --- transcript search (Phase 7) ----------------------------------------------


class SearchTranscriptsParams(_Strict):
    query: str
    hub_run_id: str | None = None
    session_id: str | None = None
    all_runs: bool = False
    limit: int = 200


class SearchTranscriptsResult(_Strict):
    matches: list[dict[str, Any]]


# --- readonly registration / idle (Phase 8) -----------------------------------


class RegisterReadonlyParams(_Strict):
    cwd: str
    claude_session_id: str
    name: str | None = None
    tags: list[str] = []


class RegisterReadonlyResult(_Strict):
    session: SessionDict


class AttachExistingReadonlyParams(_Strict):
    pid: int
    cwd: str
    name: str | None = None


class AttachExistingReadonlyResult(_Strict):
    session: SessionDict


class MarkIdleParams(_Strict):
    claude_session_id: str


# --- hub-run history / notes (Phase 9) ----------------------------------------


class ListHubRunsParams(_Strict):
    pass


class ListHubRunsResult(_Strict):
    runs: list[dict[str, Any]]


class GetHubRunParams(_Strict):
    id: str


class GetHubRunResult(_Strict):
    run: dict[str, Any]
    sessions: list[dict[str, Any]]


class SetHubRunNoteParams(_Strict):
    id: str
    note: str


# --- tags (Phase 10) ----------------------------------------------------------


class SetSessionTagsParams(_Strict):
    id: str
    add: list[str] = []
    remove: list[str] = []


# --- attach / promote / detach (Phase 11) -------------------------------------


class ScanCandidatesParams(_Strict):
    pass


class ScanCandidatesResult(_Strict):
    candidates: list[dict[str, Any]]


class AttachTmuxParams(_Strict):
    name: str
    cwd: str
    pid: int
    tmux_target: str
    tags: list[str] = []


class AttachTmuxResult(_Strict):
    session: SessionDict


class DetachSessionParams(_Strict):
    id: str


class PromoteSessionParams(_Strict):
    id: str


# --- /detach (release session from chubby) ------------------------------------


class ReleaseSessionParams(_Strict):
    """Sent by the TUI when the user types ``/detach``: capture the
    focused session's claude_session_id + cwd, then kill the wrapper
    (claude dies along with it) and remove the session from the
    in-memory registry. The caller uses the captured fields to re-open
    a fresh ``claude --resume <id>`` in a new GUI terminal — the
    JSONL transcript is the same file, so the conversation continues
    seamlessly outside chubby's management."""

    id: str


class ReleaseSessionResult(_Strict):
    claude_session_id: str
    cwd: str


# --- purge (Phase 14) ---------------------------------------------------------


class PurgeParams(_Strict):
    run_id: str | None = None
    session: str | None = None


# --- session history (TUI viewport persistence) -------------------------------


class GetSessionHistoryParams(_Strict):
    session_id: str
    limit: int = 500


class GetSessionHistoryResult(_Strict):
    turns: list[dict[str, Any]]  # [{role, text, ts}, ...]


# --- /refresh-claude — restart claude under the same wrapper ------------------


class UpdateClaudePidParams(_Strict):
    """Sent by the wrapper after a restart_claude cycle: the chubby session
    id is unchanged but the underlying claude pid moved."""

    session_id: str
    claude_pid: int


class RefreshClaudeSessionParams(_Strict):
    """Sent by the TUI when the user types ``/refresh-claude``: the
    daemon forwards a ``restart_claude`` event over the wrapper's writer,
    the wrapper SIGTERMs claude and re-launches with --resume."""

    id: str


# --- recent cwds (Phase B: spawn modal Ctrl+P recent-dir picker) -------------


class RecentCwdsParams(_Strict):
    """Sent by the TUI's spawn modal Ctrl+P: returns the most recently
    used cwds (distinct, ordered by created_at DESC) so the user can
    cycle through their recent project dirs without retyping."""

    limit: int = 20


class RecentCwdsResult(_Strict):
    cwds: list[str]


class ListAllClaudeJsonlsParams(_Strict):
    """Phase 8d cross-project history browser: scan
    ``~/.claude/projects/*/*.jsonl`` and return per-session metadata
    sorted by recency. ``limit`` bounds the result so users with
    thousands of historical sessions still get a snappy list."""

    limit: int = 200


class ClaudeJsonlEntry(_Strict):
    claude_session_id: str
    cwd: str
    first_user_message: str | None = None
    mtime_ms: int
    size: int


class ListAllClaudeJsonlsResult(_Strict):
    entries: list[ClaudeJsonlEntry]


# --- run commands (.chubby/config.json `run` array) --------------------------


class RunCommandInfo(_Strict):
    """One entry from the project's ``run`` array, with whether it's
    currently running for this session. ``pid`` and ``log_path`` are
    populated only when ``running`` is true."""

    index: int
    cmd: str
    running: bool = False
    pid: int | None = None
    log_path: str | None = None


class ListRunCommandsParams(_Strict):
    session_id: str


class ListRunCommandsResult(_Strict):
    commands: list[RunCommandInfo]


class StartRunCommandParams(_Strict):
    """``:run <index>`` from the rail palette. Index refers to the
    position in the project's ``run`` array."""

    session_id: str
    index: int


class StartRunCommandResult(_Strict):
    pid: int
    log_path: str
    cmd: str


class StopRunCommandParams(_Strict):
    session_id: str
    index: int


class StopRunCommandResult(_Strict):
    stopped: bool
