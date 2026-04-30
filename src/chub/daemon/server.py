"""asyncio Unix-socket server: framing, dispatch, peer-UID check."""

from __future__ import annotations

import asyncio
import logging
import os
import socket
import struct
from collections.abc import Awaitable, Callable
from pathlib import Path

from chub.daemon.handlers import CallContext, HandlerRegistry
from chub.proto import frame
from chub.proto.errors import ChubError, ErrorCode
from chub.proto.rpc import Request, Response, decode_message, encode_message

log = logging.getLogger(__name__)


def _peer_uid(sock: socket.socket) -> int | None:
    """Return UID of the connecting peer, or None if unsupported."""
    try:
        if hasattr(socket, "SO_PEERCRED"):
            buf = sock.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i"))
            _, uid, _ = struct.unpack("3i", buf)
            return int(uid)
    except OSError:
        pass
    try:
        from socket import getpeereid  # type: ignore[attr-defined]
        uid, _ = getpeereid(sock.fileno())
        return int(uid)
    except (ImportError, OSError):
        return None


class Server:
    def __init__(self, sock_path: Path, registry: HandlerRegistry) -> None:
        self.sock_path = sock_path
        self.registry = registry
        self._server: asyncio.Server | None = None
        self._connections: set[asyncio.Task[None]] = set()
        self._next_conn_id: int = 0
        self._write_locks: dict[int, asyncio.Lock] = {}

    async def start(self) -> None:
        if self.sock_path.exists():
            self.sock_path.unlink()
        self._server = await asyncio.start_unix_server(self._handle_conn, path=str(self.sock_path))
        os.chmod(self.sock_path, 0o600)
        log.info("chubd listening on %s", self.sock_path)

    async def stop(self) -> None:
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
        for t in list(self._connections):
            t.cancel()
        if self.sock_path.exists():
            try:
                self.sock_path.unlink()
            except FileNotFoundError:
                pass

    async def wait_closed(self) -> None:
        """Block until the underlying server closes (for any reason)."""
        if self._server is not None:
            await self._server.wait_closed()

    async def _handle_conn(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        sock: socket.socket = writer.get_extra_info("socket")
        uid = _peer_uid(sock)
        if uid is not None and uid != os.getuid():
            log.warning("rejecting connection from uid %d", uid)
            writer.close()
            return
        conn_id = self._next_conn_id
        self._next_conn_id += 1
        write_lock = asyncio.Lock()
        self._write_locks[conn_id] = write_lock

        async def _safe_write(payload: bytes) -> None:
            async with write_lock:
                await frame.write_frame(writer, payload)

        ctx = CallContext(connection_id=conn_id, write=_safe_write)
        task = asyncio.current_task()
        if task is not None:
            self._connections.add(task)
        try:
            while True:
                try:
                    raw = await frame.read_frame(reader)
                except frame.FrameTooLarge:
                    err = ChubError(ErrorCode.INVALID_PAYLOAD, "frame too large")
                    await _safe_write(encode_message(Response.from_error(0, err)))
                    return
                if raw is None:
                    return
                await self._dispatch_one(raw, _safe_write, ctx)
        except (ConnectionResetError, BrokenPipeError):
            pass
        finally:
            if task is not None:
                self._connections.discard(task)
            self._write_locks.pop(conn_id, None)
            writer.close()

    async def _dispatch_one(
        self,
        raw: bytes,
        write: Callable[[bytes], Awaitable[None]],
        ctx: CallContext,
    ) -> None:
        try:
            msg = decode_message(raw)
        except ChubError as e:
            await write(encode_message(Response.from_error(0, e)))
            return
        if not isinstance(msg, Request):
            return
        try:
            result = await self.registry.invoke(msg.method, msg.params, ctx)
        except ChubError as e:
            await write(encode_message(Response.from_error(msg.id, e)))
            return
        except Exception as e:
            log.exception("handler %r crashed", msg.method)
            err = ChubError(ErrorCode.INTERNAL, f"internal error: {e}")
            await write(encode_message(Response.from_error(msg.id, err)))
            return
        await write(encode_message(Response(id=msg.id, result=result)))
