"""asyncio Unix-socket server: framing, dispatch, peer-UID check."""

from __future__ import annotations

import asyncio
import logging
import os
import socket
import struct
from pathlib import Path

from chub.daemon.handlers import HandlerRegistry
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

    async def _handle_conn(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        sock: socket.socket = writer.get_extra_info("socket")
        uid = _peer_uid(sock)
        if uid is not None and uid != os.getuid():
            log.warning("rejecting connection from uid %d", uid)
            writer.close()
            return
        task = asyncio.current_task()
        if task is not None:
            self._connections.add(task)
        try:
            while True:
                try:
                    raw = await frame.read_frame(reader)
                except frame.FrameTooLarge:
                    err = ChubError(ErrorCode.INVALID_PAYLOAD, "frame too large")
                    await frame.write_frame(writer, encode_message(Response.from_error(0, err)))
                    return
                if raw is None:
                    return
                await self._dispatch_one(raw, writer)
        except (ConnectionResetError, BrokenPipeError):
            pass
        finally:
            if task is not None:
                self._connections.discard(task)
            writer.close()

    async def _dispatch_one(self, raw: bytes, writer: asyncio.StreamWriter) -> None:
        try:
            msg = decode_message(raw)
        except ChubError as e:
            await frame.write_frame(writer, encode_message(Response.from_error(0, e)))
            return
        if not isinstance(msg, Request):
            return
        try:
            result = await self.registry.invoke(msg.method, msg.params)
        except ChubError as e:
            await frame.write_frame(writer, encode_message(Response.from_error(msg.id, e)))
            return
        except Exception as e:
            log.exception("handler %r crashed", msg.method)
            err = ChubError(ErrorCode.INTERNAL, f"internal error: {e}")
            await frame.write_frame(writer, encode_message(Response.from_error(msg.id, err)))
            return
        await frame.write_frame(writer, encode_message(Response(id=msg.id, result=result)))
