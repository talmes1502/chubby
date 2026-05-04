"""End-to-end test for ``.chubby/config.json`` setup scripts.

Drops a config in a temp project, spawns a session through the live
daemon, asserts the setup command actually ran (sentinel file
exists in the workspace cwd) before the wrapper started.
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

import pytest

from chubby.cli.client import Client
from chubby.daemon import main as chubbyd_main


async def test_setup_script_runs_before_wrapper(
    chub_home: Path,
    fakeclaude_bin: Path,
    tmp_path: Path,
) -> None:
    """A ``.chubby/config.json`` with ``setup: ["touch SENTINEL"]``
    must produce ``SENTINEL`` in the spawn's cwd before the wrapper
    is exec'd. The user-visible promise is "my install command ran
    before claude saw the project".
    """
    project = tmp_path / "proj"
    project.mkdir()
    (project / ".chubby").mkdir()
    (project / ".chubby" / "config.json").write_text(
        json.dumps({"setup": ["touch SENTINEL"]}),
        encoding="utf-8",
    )

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
        await client.call(
            "spawn_session",
            {
                "name": "setup-test",
                "cwd": str(project),
                "tags": [],
            },
        )
        # spawn_session returns AFTER setup completes (it's awaited
        # inline before the wrapper subprocess fires), so the file
        # must exist by the time we get here.
        assert (project / "SENTINEL").exists(), "setup script did not run before wrapper spawn"
        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()


async def test_setup_failure_aborts_spawn(
    chub_home: Path,
    fakeclaude_bin: Path,
    tmp_path: Path,
) -> None:
    """A setup script that exits non-zero must surface an error from
    spawn_session — no half-spawned session, no wrapper started.
    """
    project = tmp_path / "proj"
    project.mkdir()
    (project / ".chubby").mkdir()
    (project / ".chubby" / "config.json").write_text(
        json.dumps({"setup": ["echo nope >&2; exit 1"]}),
        encoding="utf-8",
    )

    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists()

    try:
        client = Client(sock)
        with pytest.raises(Exception):
            await client.call(
                "spawn_session",
                {
                    "name": "setup-fail",
                    "cwd": str(project),
                    "tags": [],
                },
            )
        # No session should have registered.
        r = await client.call("list_sessions", {})
        names = {s["name"] for s in r["sessions"]}
        assert "setup-fail" not in names
        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
