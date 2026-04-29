"""End-to-end test for `chub up --resume` PID-alive bookkeeping.

Stands up chubd, registers a wrapped session with a clearly-dead pid, shuts
the daemon down, then restarts it with ``CHUB_RESUME=last`` and verifies the
re-imported session is marked ``dead``.
"""

from __future__ import annotations

import asyncio
import os
from pathlib import Path

from chub.cli.client import Client
from chub.daemon.main import serve as daemon_serve


async def test_resume_marks_dead_pids(chub_home: Path) -> None:
    sock = chub_home / "hub.sock"

    # --- Run 1: register a session referencing a clearly-dead pid. -----------
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
            {"name": "x", "cwd": "/tmp", "pid": 99999999},
        )
        await c.close()
    finally:
        stop.set()
        await asyncio.wait_for(t, timeout=3.0)

    # --- Run 2 with CHUB_RESUME=last: the prior session should re-appear ----
    os.environ["CHUB_RESUME"] = "last"
    try:
        stop = asyncio.Event()
        t = asyncio.create_task(daemon_serve(stop_event=stop))
        try:
            for _ in range(200):
                if sock.exists():
                    break
                await asyncio.sleep(0.02)
            assert sock.exists()
            c = Client(sock)
            r = await c.call("list_sessions", {})
            sessions = r["sessions"]
            await c.close()
        finally:
            stop.set()
            await asyncio.wait_for(t, timeout=3.0)
    finally:
        del os.environ["CHUB_RESUME"]

    assert any(
        s["name"] == "x" and s["status"] == "dead" for s in sessions
    ), f"expected resumed `x` session marked dead, got: {sessions!r}"
