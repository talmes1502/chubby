"""End-to-end test for `chubby spawn` / the daemon-side `spawn_session` RPC.

Stands up chubbyd, calls ``spawn_session`` over JSON-RPC, and verifies that a
new session is registered and reachable via ``list_sessions``. The session is
launched against a ``fakeclaude`` shim on PATH so no real Claude is contacted.
"""

from __future__ import annotations

import asyncio
import os
from pathlib import Path

from chubby.cli.client import Client
from chubby.daemon import main as chubbyd_main


async def test_spawn_session_launches_wrapper(
    chub_home: Path,
    fakeclaude_bin: Path,
    tmp_path: Path,
) -> None:
    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists()

    proc_pid: int | None = None
    try:
        client = Client(sock)
        r = await client.call(
            "spawn_session",
            {"name": "spawned", "cwd": str(tmp_path), "tags": []},
        )
        assert r["session"]["name"] == "spawned"
        proc_pid = r["session"].get("pid")

        listed = await client.call("list_sessions", {})
        assert any(s["name"] == "spawned" for s in listed["sessions"])
        await client.close()
    finally:
        if proc_pid:
            try:
                os.kill(proc_pid, 9)
            except ProcessLookupError:
                pass
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
