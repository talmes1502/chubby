"""End-to-end test for `chubby grep` — wires search_transcripts RPC into a real daemon."""

from __future__ import annotations

import asyncio
import base64
from pathlib import Path

from chubby.cli.client import Client
from chubby.daemon import main as chubbyd_main


async def test_search_transcripts_finds_pushed_output(chub_home: Path) -> None:
    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists(), "chubbyd never created its socket"

    try:
        client = Client(sock)
        # Register a wrapped session.
        r = await client.call(
            "register_wrapped",
            {"name": "frontend", "cwd": "/tmp", "pid": 1234, "tags": []},
        )
        sid = r["session"]["id"]

        # Push some output containing the search target.
        payload = b"DELAYED_QUEUE_FULL appeared in run\n"
        await client.call(
            "push_output",
            {
                "session_id": sid,
                "seq": 1,
                "data_b64": base64.b64encode(payload).decode(),
                "role": "raw",
            },
        )

        # Wait for the debounced flush (200ms).
        await asyncio.sleep(0.4)

        result = await client.call(
            "search_transcripts",
            {
                "query": "DELAYED_QUEUE_FULL",
                "session_id": None,
                "hub_run_id": None,
                "all_runs": False,
                "limit": 200,
            },
        )
        await client.close()
        matches = result["matches"]
        assert len(matches) >= 1
        assert matches[0]["session_id"] == sid
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
