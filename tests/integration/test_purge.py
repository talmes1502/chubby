"""End-to-end tests for the `purge` RPC."""

from __future__ import annotations

import asyncio
from pathlib import Path

from chub.cli.client import Client
from chub.daemon.main import serve as daemon_serve


async def test_purge_session_by_name(chub_home: Path) -> None:
    sock = chub_home / "hub.sock"
    stop = asyncio.Event()
    t = asyncio.create_task(daemon_serve(stop_event=stop))
    try:
        for _ in range(200):
            if sock.exists():
                break
            await asyncio.sleep(0.02)
        assert sock.exists()
        c = Client(sock)
        await c.call(
            "register_wrapped",
            {"name": "doomed", "cwd": "/tmp", "pid": 99999997},
        )
        listed = await c.call("list_sessions", {})
        assert any(s["name"] == "doomed" for s in listed["sessions"])

        await c.call("purge", {"session": "doomed"})

        listed_after = await c.call("list_sessions", {})
        assert not any(s["name"] == "doomed" for s in listed_after["sessions"])

        await c.close()
    finally:
        stop.set()
        await asyncio.wait_for(t, timeout=3.0)


async def test_purge_run_by_id(chub_home: Path) -> None:
    sock = chub_home / "hub.sock"
    stop = asyncio.Event()
    t = asyncio.create_task(daemon_serve(stop_event=stop))
    try:
        for _ in range(200):
            if sock.exists():
                break
            await asyncio.sleep(0.02)
        assert sock.exists()
        c = Client(sock)
        await c.call(
            "register_wrapped",
            {"name": "alpha", "cwd": "/tmp", "pid": 99999996},
        )
        runs = (await c.call("list_hub_runs", {}))["runs"]
        assert len(runs) >= 1
        # Pick a non-current run if available; otherwise the current one is OK
        # (deletion is tracked in DB only). We use the first listed run.
        target = runs[0]["id"]

        await c.call("purge", {"run_id": target})

        runs_after = (await c.call("list_hub_runs", {}))["runs"]
        assert not any(r["id"] == target for r in runs_after)

        await c.close()
    finally:
        stop.set()
        await asyncio.wait_for(t, timeout=3.0)
