"""Phase B: tests for the ``recent_cwds`` RPC used by the TUI spawn
modal's Ctrl+P picker. Distinct + most-recent-first.
"""

from __future__ import annotations

import asyncio
import json
import shutil
import tempfile
from pathlib import Path

import pytest

from chubby.daemon import main as chubbyd_main
from chubby.daemon.persistence import Database
from chubby.daemon.session import Session, SessionKind, SessionStatus
from chubby.proto import frame


@pytest.fixture
def short_home() -> Path:
    # AF_UNIX sun_path limit on macOS — pytest's tmp_path is too long
    # when used as CHUBBY_HOME (the socket sits under it).
    d = Path(tempfile.mkdtemp(prefix="chubby-"))
    try:
        yield d
    finally:
        shutil.rmtree(d, ignore_errors=True)


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


async def test_recent_cwds_persistence_distinct_and_ordered(tmp_path: Path) -> None:
    """Direct Database test — distinct cwds, ordered by most-recent
    created_at. Two sessions sharing a cwd collapse to one entry.
    """
    db = await Database.open(tmp_path / "state.db")
    try:
        # Three sessions: /a (oldest), /b (middle), /a (newest — same cwd as the
        # oldest; should bubble /a to the front when we MAX(created_at)).
        for i, (cwd, ts) in enumerate([("/a", 1000), ("/b", 2000), ("/a", 3000)]):
            await db.upsert_session(
                Session(
                    id=f"s{i}",
                    hub_run_id="hr",
                    name=f"sess{i}",
                    color="#fff",
                    kind=SessionKind.WRAPPED,
                    cwd=cwd,
                    created_at=ts,
                    last_activity_at=ts,
                    status=SessionStatus.IDLE,
                )
            )
        cwds = await db.recent_cwds(20)
        # /a wins on most-recent (ts=3000) > /b (ts=2000). Distinct.
        assert cwds == ["/a", "/b"]
    finally:
        await db.close()


async def test_recent_cwds_skips_empty(tmp_path: Path) -> None:
    """Sessions with empty cwd should never appear in recent_cwds."""
    db = await Database.open(tmp_path / "state.db")
    try:
        for i, cwd in enumerate(["/real", "", "/real"]):
            await db.upsert_session(
                Session(
                    id=f"s{i}",
                    hub_run_id="hr",
                    name=f"sess{i}",
                    color="#fff",
                    kind=SessionKind.WRAPPED,
                    cwd=cwd,
                    created_at=1000 + i,
                    last_activity_at=1000 + i,
                    status=SessionStatus.IDLE,
                )
            )
        cwds = await db.recent_cwds(20)
        assert cwds == ["/real"]
    finally:
        await db.close()


async def test_recent_cwds_respects_limit(tmp_path: Path) -> None:
    db = await Database.open(tmp_path / "state.db")
    try:
        for i, cwd in enumerate(["/a", "/b", "/c", "/d"]):
            await db.upsert_session(
                Session(
                    id=f"s{i}",
                    hub_run_id="hr",
                    name=f"sess{i}",
                    color="#fff",
                    kind=SessionKind.WRAPPED,
                    cwd=cwd,
                    created_at=1000 + i,
                    last_activity_at=1000 + i,
                    status=SessionStatus.IDLE,
                )
            )
        cwds = await db.recent_cwds(2)
        # Most-recent first: /d, /c.
        assert cwds == ["/d", "/c"]
    finally:
        await db.close()


async def test_recent_cwds_rpc_end_to_end(short_home: Path, monkeypatch) -> None:
    """End-to-end: spin the daemon, register a few wrapped sessions with
    distinct cwds, call recent_cwds over the wire and assert the result
    shape and distinct-most-recent-first ordering.
    """
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
        # Register three sessions with two distinct cwds. The newer
        # /tmp/zoo session should outrank the earlier /tmp/zoo because
        # we sort by MAX(created_at) per cwd.
        await _rpc(sock, "register_wrapped", {"name": "a", "cwd": "/tmp/zoo", "pid": 101})
        await _rpc(sock, "register_wrapped", {"name": "b", "cwd": "/tmp/bar", "pid": 102})
        await _rpc(sock, "register_wrapped", {"name": "c", "cwd": "/tmp/zoo", "pid": 103})

        out = await _rpc(sock, "recent_cwds", {"limit": 20})
        cwds = out["result"]["cwds"]
        # Distinct.
        assert sorted(set(cwds)) == sorted(cwds)
        # Both unique cwds present.
        assert set(cwds) == {"/tmp/zoo", "/tmp/bar"}
        # Most-recent first: the latest registration was c -> /tmp/zoo.
        assert cwds[0] == "/tmp/zoo"
    finally:
        stop.set()
        await server_task
