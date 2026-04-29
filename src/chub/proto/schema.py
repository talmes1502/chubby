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
