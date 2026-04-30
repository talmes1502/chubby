import asyncio
import json
import shutil
import tempfile
from pathlib import Path

import pytest

from chubby.daemon import main as chubbyd_main
from chubby.proto import frame


async def _ping(sock_path: Path) -> dict:
    reader, writer = await asyncio.open_unix_connection(str(sock_path))
    req = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "ping", "params": {}}).encode()
    writer.write(frame.encode(req))
    await writer.drain()
    raw = await frame.read_frame(reader)
    writer.close()
    await writer.wait_closed()
    assert raw is not None
    return json.loads(raw)


@pytest.fixture
def short_home() -> Path:
    # macOS AF_UNIX sun_path is limited to ~104 bytes; pytest's tmp_path is too
    # long when used as CHUBBY_HOME (the socket sits inside it). Use a short
    # /tmp dir instead.
    d = Path(tempfile.mkdtemp(prefix="chubby-"))
    try:
        yield d
    finally:
        shutil.rmtree(d, ignore_errors=True)


async def test_run_serves_ping_and_version(short_home: Path, monkeypatch) -> None:
    monkeypatch.setenv("CHUBBY_HOME", str(short_home))
    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = short_home / "hub.sock"
    for _ in range(50):
        if sock.exists():
            break
        await asyncio.sleep(0.02)
    assert sock.exists()
    try:
        ping = await _ping(sock)
        assert ping["result"]["echo"] is None
        assert "server_time_ms" in ping["result"]
    finally:
        stop.set()
        await server_task


async def _rpc(sock_path: Path, method: str, params: dict) -> dict:
    reader, writer = await asyncio.open_unix_connection(str(sock_path))
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode()
    writer.write(frame.encode(body))
    await writer.drain()
    raw = await frame.read_frame(reader)
    writer.close()
    await writer.wait_closed()
    assert raw is not None
    return json.loads(raw)


async def test_register_wrapped_then_list(short_home: Path, monkeypatch) -> None:
    monkeypatch.setenv("CHUBBY_HOME", str(short_home))
    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = short_home / "hub.sock"
    for _ in range(50):
        if sock.exists():
            break
        await asyncio.sleep(0.02)
    try:
        out = await _rpc(sock, "register_wrapped", {"name": "front", "cwd": "/tmp", "pid": 1234})
        sid = out["result"]["session"]["id"]
        listed = await _rpc(sock, "list_sessions", {})
        assert any(s["id"] == sid for s in listed["result"]["sessions"])
    finally:
        stop.set()
        await server_task


async def test_serve_exits_when_server_dies(short_home: Path, monkeypatch) -> None:
    """If the underlying asyncio.Server closes for any reason (not just
    because we set stop_event), serve() should still proceed to cleanup
    and release the PID lock so a new daemon can start.
    """
    monkeypatch.setenv("CHUBBY_HOME", str(short_home))
    pid_path = short_home / "hub.pid"

    real_start = chubbyd_main.Server.start

    async def start_then_close(self: chubbyd_main.Server) -> None:
        await real_start(self)
        # Simulate the asyncio.Server dying (e.g. because of a fatal
        # internal error in the accept loop).
        assert self._server is not None
        self._server.close()

    monkeypatch.setattr(chubbyd_main.Server, "start", start_then_close)

    stop = asyncio.Event()
    # serve() must return on its own without us having to set stop.
    await asyncio.wait_for(chubbyd_main.serve(stop_event=stop), timeout=2.0)

    # PID lock file must be gone so a fresh daemon can start.
    assert not pid_path.exists()
