"""End-to-end tests for `set_session_tags` RPC and `chubby tag` CLI."""

from __future__ import annotations

import asyncio
from pathlib import Path

from typer.testing import CliRunner

from chubby.cli.client import Client
from chubby.cli.main import app
from chubby.daemon.main import serve as daemon_serve


async def test_set_session_tags_round_trip(chub_home: Path) -> None:
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
        reg = await c.call(
            "register_wrapped",
            {
                "name": "alpha",
                "cwd": "/tmp",
                "pid": 99999998,
                "tags": ["existing"],
            },
        )
        sid = reg["session"]["id"]

        await c.call(
            "set_session_tags",
            {"id": sid, "add": ["frontend", "backend"], "remove": ["existing"]},
        )

        listed = (await c.call("list_sessions", {}))["sessions"]
        match = next(s for s in listed if s["id"] == sid)
        assert match["tags"] == ["backend", "frontend"]

        await c.close()
    finally:
        stop.set()
        await asyncio.wait_for(t, timeout=3.0)


def test_tag_command_in_help() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "tag" in result.stdout


def test_tag_rejects_unprefixed_argument() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["tag", "alpha", "no-prefix-marker"])
    assert result.exit_code != 0
