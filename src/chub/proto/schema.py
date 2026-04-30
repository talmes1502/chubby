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


class SpawnSessionParams(_Strict):
    name: str
    # Empty string means "default to $HOME" — the daemon's spawn_session
    # handler resolves it before launching the wrapper. Made optional so
    # callers (TUI spawn modal, ``chub-claude --name x`` with no --cwd)
    # don't have to invent a sensible default themselves.
    cwd: str = ""
    tags: list[str] = []


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
    """Sent by the wrapper after a restart_claude cycle: the chub session
    id is unchanged but the underlying claude pid moved."""

    session_id: str
    claude_pid: int


class RefreshClaudeSessionParams(_Strict):
    """Sent by the TUI when the user types ``/refresh-claude``: the
    daemon forwards a ``restart_claude`` event over the wrapper's writer,
    the wrapper SIGTERMs claude and re-launches with --resume."""

    id: str
