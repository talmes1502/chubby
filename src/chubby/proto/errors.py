"""Stable error codes for chubby RPCs. Codes are negative ints
(JSON-RPC server-error range -32000 to -32099 reserved; we go below)."""

from __future__ import annotations

from enum import IntEnum
from typing import Any


class ErrorCode(IntEnum):
    SESSION_NOT_FOUND = -33000
    INJECTION_NOT_SUPPORTED = -33001
    SESSION_DEAD = -33002
    WRAPPER_UNREACHABLE = -33003
    NAME_TAKEN = -33004
    INVALID_PAYLOAD = -33005
    DAEMON_BUSY = -33006
    HUB_RUN_LOCKED = -33007
    TMUX_NOT_FOUND = -33008
    TMUX_TARGET_INVALID = -33009
    ATTACH_PROMOTE_REQUIRED = -33010
    ALREADY_ATTACHED = -33011
    INTERNAL = -33099


class ChubError(Exception):
    def __init__(self, code: ErrorCode, message: str, data: dict[str, Any] | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.data = data

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"code": self.code.value, "message": self.message}
        if self.data is not None:
            out["data"] = self.data
        return out
