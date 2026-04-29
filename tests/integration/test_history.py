"""End-to-end tests for `list_hub_runs`, `get_hub_run`, `set_hub_run_note` RPCs."""

from __future__ import annotations

import asyncio
from pathlib import Path

from chub.cli.client import Client
from chub.daemon.main import serve as daemon_serve


async def test_history_and_note(chub_home: Path) -> None:
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
            {"name": "alpha", "cwd": "/tmp", "pid": 99999998},
        )

        runs = (await c.call("list_hub_runs", {}))["runs"]
        assert len(runs) >= 1
        run_id = runs[0]["id"]
        assert runs[0]["notes"] in (None, "")

        detail = await c.call("get_hub_run", {"id": run_id})
        assert detail["run"]["id"] == run_id
        assert any(s["name"] == "alpha" for s in detail["sessions"])

        await c.call("set_hub_run_note", {"id": run_id, "note": "post-mortem"})
        runs2 = (await c.call("list_hub_runs", {}))["runs"]
        match = next(r for r in runs2 if r["id"] == run_id)
        assert match["notes"] == "post-mortem"

        await c.close()
    finally:
        stop.set()
        await asyncio.wait_for(t, timeout=3.0)
