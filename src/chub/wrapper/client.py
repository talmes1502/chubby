"""Wrapper-side daemon connection. Single-connection JSON-RPC client used by
``chub-claude`` to talk to chubd: registers itself, pushes output chunks, and
listens for server-pushed ``inject_to_pty`` events."""

from __future__ import annotations

import asyncio
import base64
import logging
from pathlib import Path
from typing import Any

from chub.proto import frame
from chub.proto.errors import ChubError, ErrorCode
from chub.proto.rpc import Request, Response, decode_message, encode_message

log = logging.getLogger(__name__)


class WrapperClient:
    def __init__(self, sock_path: Path) -> None:
        self.sock_path = sock_path
        self.session_id: str | None = None
        self._reader: asyncio.StreamReader | None = None
        self._writer: asyncio.StreamWriter | None = None
        self._id_counter = 0
        self._lock = asyncio.Lock()

    async def _ensure(self) -> None:
        if self._reader is None or self._writer is None:
            self._reader, self._writer = await asyncio.open_unix_connection(
                str(self.sock_path)
            )

    async def _call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        async with self._lock:
            await self._ensure()
            assert self._writer is not None and self._reader is not None
            self._id_counter += 1
            rid = self._id_counter
            await frame.write_frame(
                self._writer, encode_message(Request(rid, method, params))
            )
            raw = await frame.read_frame(self._reader)
            if raw is None:
                self._reader = self._writer = None
                raise ChubError(ErrorCode.WRAPPER_UNREACHABLE, "daemon closed")
            msg = decode_message(raw)
            if not isinstance(msg, Response):
                raise ChubError(ErrorCode.INTERNAL, "unexpected msg")
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

    async def register(
        self, *, name: str, cwd: str, pid: int, tags: list[str]
    ) -> str:
        out = await self._call(
            "register_wrapped",
            {"name": name, "cwd": cwd, "pid": pid, "tags": tags},
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
        if self._writer is not None:
            self._writer.close()
            try:
                await self._writer.wait_closed()
            except (ConnectionResetError, BrokenPipeError):
                pass
            self._reader = self._writer = None
