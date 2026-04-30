"""Wrapper-side daemon connection. Single-connection JSON-RPC client used by
``chub-claude`` to talk to chubd: registers itself, pushes output chunks, and
listens for server-pushed ``inject_to_pty`` events.

Read and write paths are split: a writer task drains an outbound queue and a
reader task fans frames into either pending-call futures (Responses) or an
``_inbound_events`` queue (Events). ``call()`` enqueues a frame and awaits
its future. This means the lock contention that the original lock-per-call
design introduced is gone, and ``push_chunk`` plus inbound ``inject_to_pty``
no longer share a single resource."""

from __future__ import annotations

import asyncio
import base64
import logging
from pathlib import Path
from typing import Any

from chub.proto import frame
from chub.proto.errors import ChubError, ErrorCode
from chub.proto.rpc import Event, Request, Response, decode_message, encode_message

log = logging.getLogger(__name__)


class WrapperClient:
    def __init__(self, sock_path: Path) -> None:
        self.sock_path = sock_path
        self.session_id: str | None = None
        self._reader: asyncio.StreamReader | None = None
        self._writer: asyncio.StreamWriter | None = None
        self._id_counter = 0
        # Outbound: writer task drains; producers enqueue. ``None`` is the
        # close sentinel that wakes the writer to exit cleanly.
        self._outbound: asyncio.Queue[bytes | None] = asyncio.Queue()
        # Pending RPCs: reader task fulfills futures keyed by request id.
        self._pending: dict[int, asyncio.Future[Response]] = {}
        # Server-pushed events for consumers (e.g., listen_for_inject).
        self._inbound_events: asyncio.Queue[Event] = asyncio.Queue()
        self._reader_task: asyncio.Task[None] | None = None
        self._writer_task: asyncio.Task[None] | None = None
        self._closed = False
        self._connect_lock = asyncio.Lock()

    async def _ensure(self) -> None:
        async with self._connect_lock:
            if self._reader is not None and self._writer is not None:
                return
            self._reader, self._writer = await asyncio.open_unix_connection(
                str(self.sock_path)
            )
            self._reader_task = asyncio.create_task(self._read_loop())
            self._writer_task = asyncio.create_task(self._write_loop())

    async def _read_loop(self) -> None:
        reader = self._reader
        assert reader is not None
        try:
            while True:
                try:
                    raw = await frame.read_frame(reader)
                except (ConnectionResetError, BrokenPipeError):
                    raw = None
                if raw is None:
                    break
                try:
                    msg = decode_message(raw)
                except ChubError:
                    continue
                if isinstance(msg, Response):
                    fut = self._pending.pop(msg.id, None)
                    if fut is not None and not fut.done():
                        fut.set_result(msg)
                elif isinstance(msg, Event):
                    await self._inbound_events.put(msg)
                # Requests targeted at the wrapper are not part of the V1 protocol.
        finally:
            # Reader is gone — fail any in-flight calls so callers don't hang.
            err = ChubError(ErrorCode.WRAPPER_UNREACHABLE, "daemon closed")
            for fut in self._pending.values():
                if not fut.done():
                    fut.set_exception(err)
            self._pending.clear()

    async def _write_loop(self) -> None:
        writer = self._writer
        assert writer is not None
        try:
            while True:
                buf = await self._outbound.get()
                if buf is None:
                    break
                try:
                    await frame.write_frame(writer, buf)
                except (ConnectionResetError, BrokenPipeError):
                    break
        except asyncio.CancelledError:
            raise

    async def _call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        if self._closed:
            raise ChubError(ErrorCode.WRAPPER_UNREACHABLE, "client closed")
        await self._ensure()
        self._id_counter += 1
        rid = self._id_counter
        loop = asyncio.get_running_loop()
        fut: asyncio.Future[Response] = loop.create_future()
        self._pending[rid] = fut
        await self._outbound.put(encode_message(Request(rid, method, params)))
        try:
            msg = await fut
        except ChubError:
            raise
        if msg.error is not None:
            raise ChubError(
                ErrorCode(msg.error["code"]),
                msg.error["message"],
                msg.error.get("data"),
            )
        result = msg.result
        if result is None:
            return {}
        if not isinstance(result, dict):
            raise ChubError(
                ErrorCode.INTERNAL, f"unexpected result type {type(result)}"
            )
        return result

    async def events(self) -> "asyncio.Queue[Event]":
        """Return the inbound-events queue. Consumers should `await q.get()`.

        The queue is shared (single consumer expected): the wrapper main
        listens here for ``inject_to_pty`` and similar server-pushed frames.
        """
        await self._ensure()
        return self._inbound_events

    async def register(
        self,
        *,
        name: str,
        cwd: str,
        pid: int,
        tags: list[str],
        claude_pid: int | None = None,
    ) -> str:
        out = await self._call(
            "register_wrapped",
            {
                "name": name,
                "cwd": cwd,
                "pid": pid,
                "tags": tags,
                "claude_pid": claude_pid,
            },
        )
        sid = out["session"]["id"]
        if not isinstance(sid, str):
            raise ChubError(ErrorCode.INTERNAL, "register returned non-string id")
        self.session_id = sid
        return sid

    async def push_chunk(
        self, *, seq: int, data: bytes, role: str = "raw"
    ) -> None:
        if self.session_id is None:
            return
        await self._call(
            "push_output",
            {
                "session_id": self.session_id,
                "seq": seq,
                "data_b64": base64.b64encode(data).decode(),
                "role": role,
            },
        )

    async def session_ended(self) -> None:
        if self.session_id is None:
            return
        try:
            await self._call("session_ended", {"session_id": self.session_id})
        except ChubError:
            pass

    async def close(self) -> None:
        self._closed = True
        # Stop the writer task by enqueuing the close sentinel (best-effort).
        try:
            self._outbound.put_nowait(None)
        except asyncio.QueueFull:  # pragma: no cover — unbounded queue
            pass
        if self._writer is not None:
            self._writer.close()
            try:
                await self._writer.wait_closed()
            except (ConnectionResetError, BrokenPipeError):
                pass
        for task in (self._reader_task, self._writer_task):
            if task is not None and not task.done():
                task.cancel()
                try:
                    await task
                except (asyncio.CancelledError, Exception):
                    pass
        self._reader = self._writer = None
        self._reader_task = self._writer_task = None
