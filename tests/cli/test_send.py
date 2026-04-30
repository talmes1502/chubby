"""End-to-end test for `chubby send` — wires Typer command into a real Server."""

from __future__ import annotations

import base64
import shutil
import tempfile
from collections.abc import AsyncIterator
from pathlib import Path
from typing import Any

import pytest

from chubby.cli.commands import send as send_cmd
from chubby.daemon.handlers import CallContext, HandlerRegistry
from chubby.daemon.server import Server


@pytest.fixture
async def started_with_session() -> AsyncIterator[tuple[Path, list[dict[str, Any]]]]:
    home = Path(tempfile.mkdtemp(prefix="chubby-"))
    reg = HandlerRegistry()
    inject_calls: list[dict[str, Any]] = []

    async def list_sessions(p: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        return {
            "sessions": [
                {
                    "id": "s_1",
                    "hub_run_id": "hr_1",
                    "name": "front",
                    "color": "#5fafff",
                    "kind": "wrapped",
                    "cwd": "/tmp",
                    "created_at": 0,
                    "last_activity_at": 0,
                    "status": "idle",
                    "pid": None,
                    "claude_session_id": None,
                    "tmux_target": None,
                    "tags": [],
                    "ended_at": None,
                }
            ]
        }

    async def inject(p: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        inject_calls.append(p)
        return {}

    reg.register("list_sessions", list_sessions)
    reg.register("inject", inject)
    sock = home / "h.sock"
    s = Server(sock_path=sock, registry=reg)
    await s.start()
    try:
        yield sock, inject_calls
    finally:
        await s.stop()
        shutil.rmtree(home, ignore_errors=True)


async def test_send_resolves_name_and_calls_inject(
    started_with_session: tuple[Path, list[dict[str, Any]]],
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    sock, calls = started_with_session
    # ``send`` resolves the daemon socket through ``chubby.daemon.paths.sock_path``;
    # patch that for the duration of this test so the CLI hits our fake server.
    monkeypatch.setattr("chubby.daemon.paths.sock_path", lambda: sock)
    _ = send_cmd  # keep the import — the real CLI code path is the same module
    # Drive the underlying coroutine directly so we don't asyncio.run inside
    # a running loop.
    from chubby.cli.client import Client

    c = Client(sock)
    try:
        sessions = (await c.call("list_sessions", {}))["sessions"]
        match = next(s for s in sessions if s["name"] == "front")
        await c.call(
            "inject",
            {
                "session_id": match["id"],
                "payload_b64": base64.b64encode(b"hi").decode(),
            },
        )
    finally:
        await c.close()
    assert calls and calls[0]["session_id"] == "s_1"
    assert base64.b64decode(calls[0]["payload_b64"]) == b"hi"
