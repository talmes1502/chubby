"""In-memory session registry with write-through persistence to SQLite + events.ndjson."""

from __future__ import annotations

import asyncio
import base64
from collections.abc import Awaitable, Callable
from typing import Any

from chub.daemon.clock import now_ms
from chub.daemon.colors import ColorAllocator
from chub.daemon.events import EventLog
from chub.daemon.ids import new_session_id
from chub.daemon.persistence import Database
from chub.daemon.session import Session, SessionKind, SessionStatus
from chub.proto.errors import ChubError, ErrorCode
from chub.proto.rpc import Event, encode_message

WriteCallable = Callable[[bytes], Awaitable[None]]


class Registry:
    def __init__(
        self,
        hub_run_id: str,
        db: Database | None = None,
        event_log: EventLog | None = None,
    ) -> None:
        self.hub_run_id = hub_run_id
        self.db = db
        self.event_log = event_log
        self._by_id: dict[str, Session] = {}
        self._lock = asyncio.Lock()
        self._colors = ColorAllocator()
        self._preferred_color_for_name: dict[str, str] = {}
        self._wrapper_writers: dict[str, WriteCallable] = {}

    async def _persist(self, s: Session) -> None:
        if self.db is not None:
            await self.db.upsert_session(s)
            await self.db.set_preferred_color(s.name, s.color)

    async def _emit(self, event: dict[str, Any]) -> None:
        if self.event_log is not None:
            await self.event_log.append(event)

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
            preferred = self._preferred_color_for_name.get(name)
            if preferred is None and self.db is not None:
                preferred = await self.db.get_preferred_color(name)
            chosen = color or self._colors.allocate(in_use=in_use, preferred_for_name=preferred)
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
        await self._persist(s)
        await self._emit({"event": "session_added", **s.to_dict()})
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
        await self._persist(s)
        await self._emit({"event": "session_renamed", "id": s.id, "name": new_name})

    async def recolor(self, session_id: str, color: str) -> None:
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no session with id {session_id}")
            s.color = color
            self._preferred_color_for_name[s.name] = color
        await self._persist(s)
        await self._emit({"event": "session_recolored", "id": s.id, "color": color})

    async def update_status(self, session_id: str, status: SessionStatus) -> None:
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                return
            s.status = status
            s.last_activity_at = now_ms()
            if status is SessionStatus.DEAD and s.ended_at is None:
                s.ended_at = now_ms()
        await self._persist(s)
        await self._emit({"event": "session_status_changed", "id": s.id, "status": status.value})

    async def attach_wrapper(self, session_id: str, write: WriteCallable) -> None:
        """Bind a wrapper transport's write closure to a session id.

        Used so that the daemon can later push ``inject_to_pty`` events back
        through the originating wrapper's connection.
        """
        self._wrapper_writers[session_id] = write

    async def detach_wrapper(self, session_id: str) -> None:
        self._wrapper_writers.pop(session_id, None)

    async def inject(self, session_id: str, payload: bytes) -> None:
        s = await self.get(session_id)
        if s.kind is SessionKind.TMUX_ATTACHED:
            try:
                # Lazy import: the tmux attach module is wired in Phase 11.
                from importlib import import_module

                tmux_mod = import_module("chub.daemon.attach.tmux")
            except ImportError as e:
                raise ChubError(
                    ErrorCode.INJECTION_NOT_SUPPORTED,
                    "tmux attach not built yet",
                ) from e
            await tmux_mod.inject_tmux(s, payload)
            return
        write = self._wrapper_writers.get(session_id)
        if write is None:
            raise ChubError(
                ErrorCode.WRAPPER_UNREACHABLE, "no wrapper for session"
            )
        await write(
            encode_message(
                Event(
                    method="inject_to_pty",
                    params={
                        "session_id": session_id,
                        "payload_b64": base64.b64encode(payload).decode(),
                    },
                )
            )
        )
