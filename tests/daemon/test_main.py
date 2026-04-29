import asyncio
import json
import shutil
import tempfile
from pathlib import Path

import pytest

from chub.daemon import main as chubd_main
from chub.proto import frame


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
    # long when used as CHUB_HOME (the socket sits inside it). Use a short
    # /tmp dir instead.
    d = Path(tempfile.mkdtemp(prefix="chub-"))
    try:
        yield d
    finally:
        shutil.rmtree(d, ignore_errors=True)


async def test_run_serves_ping_and_version(short_home: Path, monkeypatch) -> None:
    monkeypatch.setenv("CHUB_HOME", str(short_home))
    stop = asyncio.Event()
    server_task = asyncio.create_task(chubd_main.serve(stop_event=stop))
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
