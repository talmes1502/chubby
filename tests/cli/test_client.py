"""Tests for the JSON-RPC client."""

from __future__ import annotations

import shutil
import tempfile
from collections.abc import AsyncIterator
from pathlib import Path
from typing import Any

import pytest

from chubby.cli.client import Client
from chubby.daemon.handlers import CallContext, HandlerRegistry
from chubby.daemon.server import Server
from chubby.proto.errors import ChubError, ErrorCode


@pytest.fixture
async def started() -> AsyncIterator[Path]:
    # macOS AF_UNIX sun_path limit; use a short tempdir.
    home = Path(tempfile.mkdtemp(prefix="chubby-"))
    reg = HandlerRegistry()

    async def echo(p: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        return {"echo": p.get("x")}

    async def boom(p: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        raise ChubError(ErrorCode.SESSION_NOT_FOUND, "missing")

    reg.register("echo", echo)
    reg.register("boom", boom)
    sock = home / "h.sock"
    s = Server(sock_path=sock, registry=reg)
    await s.start()
    try:
        yield sock
    finally:
        await s.stop()
        shutil.rmtree(home, ignore_errors=True)


async def test_call_round_trip(started: Path) -> None:
    c = Client(started)
    out = await c.call("echo", {"x": "hi"})
    assert out == {"echo": "hi"}
    await c.close()


async def test_call_raises_chuberror(started: Path) -> None:
    c = Client(started)
    with pytest.raises(ChubError) as exc:
        await c.call("boom", {})
    assert exc.value.code is ErrorCode.SESSION_NOT_FOUND
    await c.close()
