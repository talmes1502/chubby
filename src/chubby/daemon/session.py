"""Session domain model."""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Any


class SessionKind(str, Enum):
    WRAPPED = "wrapped"
    SPAWNED = "spawned"
    TMUX_ATTACHED = "tmux_attached"
    READONLY = "readonly"


class SessionStatus(str, Enum):
    IDLE = "idle"
    THINKING = "thinking"
    AWAITING_USER = "awaiting_user"
    DEAD = "dead"


@dataclass
class Session:
    id: str
    hub_run_id: str
    name: str
    color: str
    kind: SessionKind
    cwd: str
    created_at: int
    last_activity_at: int
    status: SessionStatus = SessionStatus.IDLE
    pid: int | None = None
    claude_session_id: str | None = None
    tmux_target: str | None = None
    tags: list[str] = field(default_factory=list)
    ended_at: int | None = None

    def to_dict(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "hub_run_id": self.hub_run_id,
            "name": self.name,
            "color": self.color,
            "kind": self.kind.value,
            "cwd": self.cwd,
            "created_at": self.created_at,
            "last_activity_at": self.last_activity_at,
            "status": self.status.value,
            "pid": self.pid,
            "claude_session_id": self.claude_session_id,
            "tmux_target": self.tmux_target,
            "tags": list(self.tags),
            "ended_at": self.ended_at,
        }
