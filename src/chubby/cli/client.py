"""JSON-RPC client over Unix socket."""

from __future__ import annotations

import asyncio
import itertools
from pathlib import Path
from typing import Any

from chubby.proto import frame
from chubby.proto.errors import ChubError, ErrorCode
from chubby.proto.rpc import Request, Response, decode_message, encode_message


class Client:
    def __init__(self, sock_path: Path) -> None:
        self.sock_path = sock_path
        self._reader: asyncio.StreamReader | None = None
        self._writer: asyncio.StreamWriter | None = None
        self._ids = itertools.count(1)

    async def _connect(self) -> tuple[asyncio.StreamReader, asyncio.StreamWriter]:
        if self._reader is None or self._writer is None:
            self._reader, self._writer = await asyncio.open_unix_connection(
                str(self.sock_path)
            )
        return self._reader, self._writer

    async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        reader, writer = await self._connect()
        rid = next(self._ids)
        await frame.write_frame(
            writer, encode_message(Request(id=rid, method=method, params=params))
        )
        raw = await frame.read_frame(reader)
        if raw is None:
            raise ChubError(ErrorCode.INTERNAL, "daemon closed connection")
        msg = decode_message(raw)
        if not isinstance(msg, Response):
            raise ChubError(ErrorCode.INTERNAL, f"unexpected message {msg}")
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
            raise ChubError(ErrorCode.INTERNAL, f"unexpected result type {type(result)}")
        return result

    async def close(self) -> None:
        if self._writer is not None:
            self._writer.close()
            await self._writer.wait_closed()
            self._reader = self._writer = None
