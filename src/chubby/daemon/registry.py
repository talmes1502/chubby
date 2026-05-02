"""In-memory session registry with write-through persistence to SQLite + events.ndjson."""

from __future__ import annotations

import asyncio
import base64
from collections.abc import Awaitable, Callable
from typing import Any

from chubby.daemon.clock import now_ms
from chubby.daemon.colors import ColorAllocator
from chubby.daemon.events import EventLog
from chubby.daemon.ids import new_session_id
from chubby.daemon.logs import LogWriter
from chubby.daemon.persistence import Database
from chubby.daemon.session import Session, SessionKind, SessionStatus
from chubby.daemon.subscriptions import SubscriptionHub
from chubby.proto.errors import ChubError, ErrorCode
from chubby.proto.rpc import Event, encode_message

WriteCallable = Callable[[bytes], Awaitable[None]]


class Registry:
    def __init__(
        self,
        hub_run_id: str,
        db: Database | None = None,
        event_log: EventLog | None = None,
        subs: SubscriptionHub | None = None,
    ) -> None:
        self.hub_run_id = hub_run_id
        self.db = db
        self.event_log = event_log
        self.subs = subs
        self._by_id: dict[str, Session] = {}
        self._lock = asyncio.Lock()
        self._colors = ColorAllocator()
        self._preferred_color_for_name: dict[str, str] = {}
        self._wrapper_writers: dict[str, WriteCallable] = {}
        self._writers: dict[str, LogWriter] = {}
        self._buffers: dict[str, bytearray] = {}
        self._flush_tasks: dict[str, asyncio.Task[None]] = {}
        self._notify_tasks: set[asyncio.Task[None]] = set()
        self._tmux_stops: dict[str, asyncio.Event] = {}

    async def _persist(self, s: Session) -> None:
        if self.db is not None:
            await self.db.upsert_session(s)
            await self.db.set_preferred_color(s.name, s.color)

    async def _emit(self, event: dict[str, Any]) -> None:
        if self.event_log is not None:
            await self.event_log.append(event)
        if self.subs is not None:
            ev = dict(event)
            kind = ev.pop("event", None) or ev.pop("kind", None)
            if kind:
                await self.subs.broadcast(kind, ev)

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

    async def set_tags(
        self, session_id: str, *, add: list[str], remove: list[str]
    ) -> None:
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                raise ChubError(
                    ErrorCode.SESSION_NOT_FOUND, f"no session {session_id}"
                )
            cur = set(s.tags)
            cur.update(add)
            cur.difference_update(remove)
            s.tags = sorted(cur)
        await self._persist(s)
        await self._emit({"event": "session_tagged", "id": s.id, "tags": s.tags})

    async def set_claude_session_id(
        self, session_id: str, claude_session_id: str
    ) -> None:
        """Bind a Claude transcript session id to a chubby session.

        Used by ``watch_for_transcript`` once it has discovered the
        JSONL file for a wrapped/spawned session. Persists the new id
        and broadcasts ``session_id_resolved`` so the TUI can take
        whatever action it wants on resolution.
        """
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                raise ChubError(
                    ErrorCode.SESSION_NOT_FOUND, f"no session with id {session_id}"
                )
            s.claude_session_id = claude_session_id
        await self._persist(s)
        await self._emit(
            {
                "event": "session_id_resolved",
                "id": s.id,
                "claude_session_id": claude_session_id,
            }
        )

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
        if status is SessionStatus.AWAITING_USER:
            from chubby.daemon.notify import notify

            task = asyncio.create_task(
                notify(f"chubby: {s.name}", "session is awaiting your input")
            )
            self._notify_tasks.add(task)
            task.add_done_callback(self._notify_tasks.discard)

    async def remove_session(self, sid: str) -> None:
        """Drop a session from the in-memory registry entirely.

        Used by ``release_session`` (the daemon side of ``/detach``) so
        the released session disappears from ``list_sessions`` and the
        TUI's rail. Cancels any pending flush task, drops writers/
        buffers, and broadcasts ``session_removed`` so subscribers can
        update their views without a list refresh round-trip.

        Note: this does NOT delete persisted history (the SQLite row,
        the JSONL transcript on disk). It only purges the live state
        — purging history is what ``purge`` is for.
        """
        async with self._lock:
            self._by_id.pop(sid, None)
            self._wrapper_writers.pop(sid, None)
            self._writers.pop(sid, None)
            self._buffers.pop(sid, None)
            self._tmux_stops.pop(sid, None)
            t = self._flush_tasks.pop(sid, None)
            if t is not None and not t.done():
                t.cancel()
        if self.subs is not None:
            await self.subs.broadcast("session_removed", {"id": sid})

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
            from chubby.daemon.attach.tmux import inject_tmux

            await inject_tmux(s, payload)
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

    async def resize(self, session_id: str, rows: int, cols: int) -> None:
        """Forward a window-size change to the wrapper for ``session_id``.

        The wrapper applies it to its PTY via ``pty.setwinsize``,
        which sends SIGWINCH to claude. Used by the live PTY pane so
        every TUI client's view-frame size is reflected back to the
        running claude.
        """
        s = await self.get(session_id)
        if s.kind is SessionKind.TMUX_ATTACHED:
            # Tmux owns its own resize loop; chubby doesn't drive it.
            return
        write = self._wrapper_writers.get(session_id)
        if write is None:
            raise ChubError(
                ErrorCode.WRAPPER_UNREACHABLE, "no wrapper for session"
            )
        await write(
            encode_message(
                Event(
                    method="resize_pty",
                    params={
                        "session_id": session_id,
                        "rows": int(rows),
                        "cols": int(cols),
                    },
                )
            )
        )

    async def attach_log_writer(self, session_id: str, writer: LogWriter) -> None:
        self._writers[session_id] = writer
        self._buffers[session_id] = bytearray()

    async def record_chunk(self, session_id: str, data: bytes, role: str) -> None:
        w = self._writers.get(session_id)
        if w is None:
            return
        await w.append(data)
        self._buffers.setdefault(session_id, bytearray()).extend(data)
        # Broadcast the raw PTY chunk so live TUI subscribers can pump
        # it through their per-session vt emulator. Base64 because
        # JSON-RPC params don't tolerate arbitrary bytes (PTY output
        # contains ANSI escapes, NUL, etc.). Subscribers that don't
        # care about PTY chunks (legacy / FTS-only consumers) ignore
        # the event method.
        if self.subs is not None:
            await self.subs.broadcast(
                "pty_chunk",
                {
                    "session_id": session_id,
                    "chunk_b64": base64.b64encode(bytes(data)).decode("ascii"),
                    "role": role,
                    "ts": now_ms(),
                },
            )
        task = self._flush_tasks.get(session_id)
        if task and not task.done():
            task.cancel()
        self._flush_tasks[session_id] = asyncio.create_task(
            self._delayed_flush(session_id, role)
        )

    async def _delayed_flush(self, session_id: str, role: str) -> None:
        try:
            await asyncio.sleep(0.2)
        except asyncio.CancelledError:
            return
        await self.flush_message(session_id, role=role)

    async def flush_message(self, session_id: str, *, role: str) -> None:
        if self.db is None:
            return
        buf = self._buffers.get(session_id)
        if not buf:
            return
        text = buf.decode("utf-8", errors="replace")
        self._buffers[session_id] = bytearray()
        await self.db.insert_message(
            session_id=session_id,
            hub_run_id=self.hub_run_id,
            ts=now_ms(),
            role=role,
            content=text,
        )
