import asyncio
import json
import os
import socket
import tempfile
from pathlib import Path

import pytest

from chub.daemon.handlers import CallContext, HandlerRegistry
from chub.daemon.server import Server
from chub.proto import frame


@pytest.fixture
async def started_server(tmp_path: Path):
    # macOS AF_UNIX path limit is ~104 chars; pytest tmp_path is too long, so
    # use a short /tmp dir for the socket itself.
    short_dir = Path(tempfile.mkdtemp(prefix="chub-"))
    sock_path = short_dir / "h.sock"
    reg = HandlerRegistry()

    async def ping(params: dict, ctx: CallContext) -> dict:
        return {"echo": params.get("message"), "server_time_ms": 1}

    reg.register("ping", ping)
    server = Server(sock_path=sock_path, registry=reg)
    await server.start()
    try:
        yield sock_path
    finally:
        await server.stop()
        try:
            short_dir.rmdir()
        except OSError:
            pass


async def _rpc(sock_path: Path, method: str, params: dict) -> dict:
    reader, writer = await asyncio.open_unix_connection(str(sock_path))
    req = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode()
    writer.write(frame.encode(req))
    await writer.drain()
    raw = await frame.read_frame(reader)
    writer.close()
    await writer.wait_closed()
    assert raw is not None
    return json.loads(raw)


async def test_socket_file_exists_with_perms_0600(started_server: Path) -> None:
    mode = started_server.stat().st_mode & 0o777
    assert mode == 0o600


async def test_ping_round_trip(started_server: Path) -> None:
    resp = await _rpc(started_server, "ping", {"message": "hi"})
    assert resp["id"] == 1
    assert resp["result"]["echo"] == "hi"


async def test_unknown_method_returns_error(started_server: Path) -> None:
    resp = await _rpc(started_server, "nope", {})
    assert resp["id"] == 1
    assert resp["error"]["code"] == -33005   # INVALID_PAYLOAD


async def test_uid_check_accepts_self(started_server: Path) -> None:
    s = socket.socket(socket.AF_UNIX)
    s.connect(str(started_server))
    s.close()
