"""Pydantic models for every RPC's params and result. New RPCs append here."""

from __future__ import annotations

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
