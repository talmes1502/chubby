"""Tests for WrapperClient: register + push_chunk against a real Server."""

from __future__ import annotations

import asyncio
import shutil
import tempfile
from pathlib import Path
from typing import Any

from chub.daemon.handlers import CallContext, HandlerRegistry
from chub.daemon.server import Server
from chub.wrapper.client import WrapperClient


async def test_register_and_send_chunk() -> None:
    short_dir = Path(tempfile.mkdtemp(prefix="chub-"))
    try:
        sock_path = short_dir / "h.sock"
        reg = HandlerRegistry()
        received: list[dict[str, Any]] = []

        async def register_wrapped(p: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
            return {
                "session": {
                    "id": "s_1",
                    "hub_run_id": "hr_1",
                    "name": p["name"],
                    "color": "#5fafff",
                    "kind": "wrapped",
                    "cwd": p["cwd"],
                    "created_at": 0,
                    "last_activity_at": 0,
                    "status": "idle",
                    "pid": p["pid"],
                    "claude_session_id": None,
                    "tmux_target": None,
                    "tags": [],
                    "ended_at": None,
                }
            }

        async def push_output(p: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
            received.append(p)
            return {}

        reg.register("register_wrapped", register_wrapped)
        reg.register("push_output", push_output)

        s = Server(sock_path=sock_path, registry=reg)
        await s.start()
        try:
            client = WrapperClient(sock_path)
            sid = await client.register(name="x", cwd="/tmp", pid=1, tags=[])
            assert sid == "s_1"
            await client.push_chunk(seq=1, data=b"hello")
            await client.close()
            await asyncio.sleep(0.05)
            assert received and received[0]["seq"] == 1
            assert received[0]["data_b64"]
        finally:
            await s.stop()
    finally:
        shutil.rmtree(short_dir, ignore_errors=True)
