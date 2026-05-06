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
        # _pty_ring keeps the most recent PTY bytes per session
        # (capped at _pty_ring_cap) so a TUI that attaches mid-session
        # can replay enough state to reconstruct claude's current
        # screen. Distinct from _buffers, which is the FTS-flush
        # staging area and gets cleared every 200 ms.
        self._pty_ring: dict[str, bytearray] = {}
        self._flush_tasks: dict[str, asyncio.Task[None]] = {}
        self._notify_tasks: set[asyncio.Task[None]] = set()
        self._tmux_stops: dict[str, asyncio.Event] = {}
        # Per-session "claude_session_id is bound" event. Lazily
        # created by wait_for_claude_binding; set by
        # set_claude_session_id. Replaces the prior 100ms polling
        # loop in release_session / refresh_claude_session — callers
        # block on the event and wake the instant binding lands
        # instead of paying ~50ms average latency on a polling tick.
        self._bind_events: dict[str, asyncio.Event] = {}

    # _pty_ring_cap bounds memory per session — claude's tighter
    # outputs (200-300 bytes per turn) mean 64 KB easily holds a
    # dozen turns of scrollback, which is more than enough to
    # reconstruct the visible screen on attach.
    _pty_ring_cap = 64 * 1024

    async def _persist(self, s: Session) -> None:
        if self.db is not None:
            await self.db.upsert_session(s)
            await self.db.set_preferred_color(s.name, s.color)

    async def set_ports(self, session_id: str, ports: list[dict[str, Any]]) -> bool:
        """Update a session's transient listening-ports cache and emit
        ``session_ports_changed`` if anything changed. Returns True if
        an event was emitted (used by tests).

        Compares by ``(port, pid)`` tuples ignoring ``address``, since
        a server flapping between IPv4 and IPv6 listeners shouldn't
        cause spurious rail updates.
        """

        def _key_set(items: list[dict[str, Any]]) -> set[tuple[int, int]]:
            return {(int(i["port"]), int(i["pid"])) for i in items}

        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                return False
            if _key_set(s.ports) == _key_set(ports):
                # No change in the (port, pid) set — skip the event.
                # (Address may have flipped from 0.0.0.0 → 127.0.0.1
                # but the user-visible state is the same.)
                s.ports = list(ports)
                return False
            s.ports = list(ports)
        await self._emit(
            {
                "event": "session_ports_changed",
                "id": session_id,
                "ports": list(ports),
            }
        )
        return True

    async def set_first_preview(self, session_id: str, text: str) -> bool:
        """Cache the first user-turn from a session's transcript on
        the Session and broadcast ``session_first_preview_resolved``
        if it changed. Idempotent — calling with the same text is a
        no-op so the watch_for_transcript loop can re-call without
        spamming events."""
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                return False
            if s.first_user_message == text:
                return False
            s.first_user_message = text
        await self._persist(s)
        await self._emit(
            {
                "event": "session_first_preview_resolved",
                "id": session_id,
                "first_user_message": text,
            }
        )
        return True

    async def set_worktree_path(self, session_id: str, path: str) -> None:
        """Record the chubby-managed worktree path for a session, so
        ``release_session`` / ``detach_session`` can clean it up. Called
        from ``spawn_session`` once the wrapper has registered. The
        worktree was already created by the time we get here — this
        only persists the *path* so cleanup survives a daemon
        restart."""
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                return
            s.worktree_path = path
        await self._persist(s)

    async def set_git_status(self, session_id: str, ahead: int | None, behind: int | None) -> bool:
        """Update a session's transient ahead/behind counts and emit
        ``session_git_status_changed`` if anything changed. Returns
        True if an event was emitted (used by tests).

        Counts are not persisted to the DB — they're snapshot-only.
        Calls with the same values as the cached state are no-ops so
        the sweep doesn't spam events.
        """
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                return False
            if s.git_ahead == ahead and s.git_behind == behind:
                return False
            s.git_ahead = ahead
            s.git_behind = behind
        await self._emit(
            {
                "event": "session_git_status_changed",
                "id": session_id,
                "ahead": ahead,
                "behind": behind,
            }
        )
        return True

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
            if any(
                s.name == name for s in self._by_id.values() if s.status is not SessionStatus.DEAD
            ):
                raise ChubError(
                    ErrorCode.NAME_TAKEN, f"name `{name}` is already in use", {"name": name}
                )
            # Evict any dead session with this name. The new live row
            # takes over the name → keeping the dead row would create
            # two-rows-same-name in the rail, with the user's mental
            # model breaking when Ctrl+P "respawns" a dead session.
            # The dead row's folder assignment is migrated to the new
            # session id below so the respawn lands in the same folder.
            evicted_dead_ids: list[str] = []
            for old in list(self._by_id.values()):
                if old.name == name and old.status is SessionStatus.DEAD:
                    del self._by_id[old.id]
                    evicted_dead_ids.append(old.id)
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
        # Tell subscribers the dead rows are gone before announcing the
        # new one; the rail rebuilds from list_sessions on each event,
        # so order isn't strictly required, but session_removed first
        # avoids one frame of "two rows, same name".
        for old_id in evicted_dead_ids:
            await self._emit({"event": "session_removed", "id": old_id})
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
            if any(
                o.name == new_name and o.id != session_id and o.status is not SessionStatus.DEAD
                for o in self._by_id.values()
            ):
                raise ChubError(
                    ErrorCode.NAME_TAKEN, f"name `{new_name}` is already in use", {"name": new_name}
                )
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

    async def set_tags(self, session_id: str, *, add: list[str], remove: list[str]) -> None:
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no session {session_id}")
            cur = set(s.tags)
            cur.update(add)
            cur.difference_update(remove)
            s.tags = sorted(cur)
        await self._persist(s)
        await self._emit({"event": "session_tagged", "id": s.id, "tags": s.tags})

    async def set_claude_session_id(self, session_id: str, claude_session_id: str) -> None:
        """Bind a Claude transcript session id to a chubby session.

        Used by ``watch_for_transcript`` once it has discovered the
        JSONL file for a wrapped/spawned session. Persists the new id,
        broadcasts ``session_id_resolved``, and wakes any RPC handler
        that's awaiting the binding (release_session,
        refresh_claude_session) via the per-session bind event.
        """
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no session with id {session_id}")
            s.claude_session_id = claude_session_id
            ev = self._bind_events.get(session_id)
        await self._persist(s)
        # Wake awaiters AFTER persisting so a handler that's racing
        # the bind reads the same state we just committed (a future
        # reg.get returns the persisted Session). Setting outside
        # the lock is fine — Event.set is atomic and waiters re-fetch
        # via reg.get() under the lock anyway.
        if ev is not None:
            ev.set()
        await self._emit(
            {
                "event": "session_id_resolved",
                "id": s.id,
                "claude_session_id": claude_session_id,
            }
        )

    async def wait_for_claude_binding(self, session_id: str, timeout: float) -> bool:
        """Block until ``claude_session_id`` is bound on ``session_id``,
        or ``timeout`` seconds elapse. Returns True on success, False
        on timeout. Returns True immediately if already bound.

        Replaces the polling loop in release_session /
        refresh_claude_session. Event-driven: ``set_claude_session_id``
        fires the per-session asyncio.Event and the awaiter wakes
        within microseconds instead of up to 100ms.

        Cleans up the event after waking so the dict doesn't grow
        unbounded across many spawn/release cycles.
        """
        async with self._lock:
            s = self._by_id.get(session_id)
            if s is None:
                raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no session with id {session_id}")
            if s.claude_session_id:
                return True
            ev = self._bind_events.get(session_id)
            if ev is None:
                ev = asyncio.Event()
                self._bind_events[session_id] = ev
        try:
            await asyncio.wait_for(ev.wait(), timeout=timeout)
        except TimeoutError:
            return False
        finally:
            # Drop the event from the registry once we're done with
            # it. set_claude_session_id only fires once per session,
            # so no future awaiter needs this entry.
            async with self._lock:
                self._bind_events.pop(session_id, None)
        return True

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
            # Force a full redraw of claude's screen at every turn-end.
            # Claude's diff-render skips cells it thinks haven't changed,
            # which is correct for real terminals (old content scrolls
            # into scrollback) but leaves ghost text in chubby's bounded
            # vt grid (the cells the user typed in before submission).
            # Two-step: clear chubby's vt first (so claude's redraw lands
            # on a blank grid), then SIGWINCH the wrapped claude to
            # trigger its standard "resize → redraw from scratch" path.
            await self._force_claude_redraw(session_id)

    async def _force_claude_redraw(self, session_id: str) -> None:
        """Push an erase-display chunk to TUI subscribers, then
        SIGWINCH the wrapped claude so it redraws every cell. Called
        on each status flip to AWAITING_USER. Best-effort: a missed
        redraw is cosmetic, not functional."""
        # Step 1: clear chubby's vt grid via a synthetic pty_chunk.
        # \x1b[2J erases display, \x1b[3J flushes scrollback,
        # \x1b[H homes cursor.
        try:
            await self.record_chunk(session_id, b"\x1b[2J\x1b[3J\x1b[H", role="raw")
        except Exception:
            pass
        # Step 2: SIGWINCH claude via the wrapper. Same shape as
        # resize_pty but with no params — the wrapper handles it as
        # a redraw signal, not a resize.
        write = self._wrapper_writers.get(session_id)
        if write is None:
            return
        try:
            await write(
                encode_message(
                    Event(
                        method="redraw_claude",
                        params={"session_id": session_id},
                    )
                )
            )
        except Exception:
            pass

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
            self._pty_ring.pop(sid, None)
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

    async def inject(self, session_id: str, payload: bytes, *, auto_newline: bool = True) -> None:
        """Forward bytes to the wrapped session's PTY.

        ``auto_newline`` (default True for the legacy compose-bar
        flow) tells the wrapper to append \\r if the payload doesn't
        already end in one — handy when sending typed-text prompts.
        For per-keystroke routing (the embedded-PTY pane) the caller
        passes False so each char isn't auto-submitted. Without this
        flag, every keystroke became its own prompt because "k" got
        rewritten to "k\\r".
        """
        s = await self.get(session_id)
        if s.kind is SessionKind.TMUX_ATTACHED:
            from chubby.daemon.attach.tmux import inject_tmux

            await inject_tmux(s, payload)
            return
        write = self._wrapper_writers.get(session_id)
        if write is None:
            raise ChubError(ErrorCode.WRAPPER_UNREACHABLE, "no wrapper for session")
        await write(
            encode_message(
                Event(
                    method="inject_to_pty",
                    params={
                        "session_id": session_id,
                        "payload_b64": base64.b64encode(payload).decode(),
                        "auto_newline": auto_newline,
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
            raise ChubError(ErrorCode.WRAPPER_UNREACHABLE, "no wrapper for session")
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

    def get_pty_buffer(self, session_id: str) -> bytes:
        """Return the bounded PTY replay ring for a session — recent
        bytes the TUI can pump through its vt emulator to reconstruct
        the current screen on attach. Empty bytes if the session has
        no recorded output yet.
        """
        ring = self._pty_ring.get(session_id)
        if ring is None:
            return b""
        return bytes(ring)

    async def attach_log_writer(self, session_id: str, writer: LogWriter) -> None:
        self._writers[session_id] = writer
        self._buffers[session_id] = bytearray()

    async def record_chunk(self, session_id: str, data: bytes, role: str) -> None:
        w = self._writers.get(session_id)
        if w is None:
            return
        await w.append(data)
        self._buffers.setdefault(session_id, bytearray()).extend(data)
        # Append to the bounded replay ring so a TUI attaching mid-
        # session can prime its vt emulator with the recent state.
        ring = self._pty_ring.setdefault(session_id, bytearray())
        ring.extend(data)
        if len(ring) > self._pty_ring_cap:
            # Trim to last cap bytes — keep the tail so the most
            # recent screen state is intact.
            del ring[: len(ring) - self._pty_ring_cap]
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
        self._flush_tasks[session_id] = asyncio.create_task(self._delayed_flush(session_id, role))

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
