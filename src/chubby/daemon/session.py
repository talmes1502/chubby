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
    # Project lifecycle setup script failed before the wrapper ever
    # got to register. The session entry exists so the user can see
    # the failure (and the captured output tail) in the rail; force-
    # delete to clear without running teardown.
    SETUP_FAILED = "setup_failed"


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
    # Transient: populated by the git-status sweep, not persisted to
    # the DB. ``None`` means "no upstream / not a repo / not yet
    # polled". Use ``repr=False`` to keep ``__repr__`` quiet.
    git_ahead: int | None = field(default=None, repr=False)
    git_behind: int | None = field(default=None, repr=False)
    # Set when the session was spawned with ``--branch`` (or ``--pr``)
    # so the daemon owns a chubby-managed git worktree at this path.
    # Used by release_session/detach_session to ``git worktree remove``
    # on cleanup. ``None`` means "session uses the user's plain cwd"
    # (the historical default).
    worktree_path: str | None = None
    # Transient list of detected listening ports under this session's
    # process tree, populated by the periodic port sweep. Each entry:
    # ``{"port": int, "pid": int, "address": str}``. Not persisted to
    # the DB — recomputed on every daemon start. ``[]`` means "nothing
    # listening / not yet polled".
    ports: list[dict[str, Any]] = field(default_factory=list, repr=False)
    # Cached first user-turn from the JSONL transcript, populated once
    # by the watch_for_transcript hook after the JSONL binds. Surfaces
    # in the TUI quick switcher and rail tooltip so users can identify
    # a session by its opening prompt rather than its name. ``None``
    # means "not yet bound / no user turn yet". Persisted via the
    # additive migration so it survives daemon restarts.
    first_user_message: str | None = field(default=None, repr=False)

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
            "git_ahead": self.git_ahead,
            "git_behind": self.git_behind,
            "worktree_path": self.worktree_path,
            "ports": list(self.ports),
            "first_user_message": self.first_user_message,
        }
