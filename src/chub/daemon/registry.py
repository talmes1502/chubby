"""In-memory session registry. Persistence is layered on in Phase 4."""

from __future__ import annotations

import asyncio

from chub.daemon.clock import now_ms
from chub.daemon.colors import ColorAllocator
from chub.daemon.ids import new_session_id
from chub.daemon.session import Session, SessionKind, SessionStatus
from chub.proto.errors import ChubError, ErrorCode


class Registry:
    def __init__(self, hub_run_id: str) -> None:
        self.hub_run_id = hub_run_id
        self._by_id: dict[str, Session] = {}
        self._lock = asyncio.Lock()
        self._colors = ColorAllocator()
        self._preferred_color_for_name: dict[str, str] = {}

    async def register(
        self,
        *,
        name: str,
        kind: SessionKind,
        cwd: str,
        pid: int | None = None,
        claude_session_id: str | None = None,
        tmux_target: str | None = None,
        tags: list[str] | None = None,
        color: str | None = None,
    ) -> Session:
        async with self._lock:
            if any(s.name == name for s in self._by_id.values() if s.status is not SessionStatus.DEAD):
                raise ChubError(ErrorCode.NAME_TAKEN, f"name `{name}` is already in use", {"name": name})
            in_use = {s.color for s in self._by_id.values() if s.status is not SessionStatus.DEAD}
            chosen = color or self._colors.allocate(
                in_use=in_use,
                preferred_for_name=self._preferred_color_for_name.get(name),
            )
            now = now_ms()
            s = Session(
                id=new_session_id(),
                hub_run_id=self.hub_run_id,
                name=name,
                color=chosen,
                kind=kind,
                cwd=cwd,
                created_at=now,
                last_activity_at=now,
                pid=pid,
                claude_session_id=claude_session_id,
                tmux_target=tmux_target,
                tags=list(tags or []),
            )
            self._by_id[s.id] = s
            self._preferred_color_for_name[name] = chosen
            return s

    async def get(self, session_id: str) -> Session:
        s = self._by_id.get(session_id)
        if s is None:
            raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no session with id {session_id}")
        return s

    async def get_by_name(self, name: str) -> Session:
        async with self._lock:
            for s in self._by_id.values():
                if s.name == name and s.status is not SessionStatus.DEAD:
                    return s
        raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no session named `{name}`", {"name": name})

    async def list_all(self) -> list[Session]:
        async with self._lock:
            return sorted(self._by_id.values(), key=lambda s: s.created_at)

    async def rename(self, session_id: str, new_name: str) -> None:
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no session with id {session_id}")
            if any(o.name == new_name and o.id != session_id and o.status is not SessionStatus.DEAD
                   for o in self._by_id.values()):
                raise ChubError(ErrorCode.NAME_TAKEN, f"name `{new_name}` is already in use", {"name": new_name})
            s.name = new_name

    async def recolor(self, session_id: str, color: str) -> None:
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no session with id {session_id}")
            s.color = color
            self._preferred_color_for_name[s.name] = color

    async def update_status(self, session_id: str, status: SessionStatus) -> None:
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                return
            s.status = status
            s.last_activity_at = now_ms()
            if status is SessionStatus.DEAD and s.ended_at is None:
                s.ended_at = now_ms()
