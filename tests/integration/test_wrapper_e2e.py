"""End-to-end test for the chub-claude wrapper.

Spawns chubd in-process, launches a real ``chub-claude`` subprocess wired to a
``fakeclaude`` shim on PATH, then verifies that an ``inject`` RPC reaches the
fake claude (it appears in ``FAKECLAUDE_LOG``).

This test is sensitive to PTY/asyncio interaction; if it deadlocks we
forcibly kill the subprocess in finally so we never leak children.
"""

from __future__ import annotations

import asyncio
import base64
import os
from pathlib import Path

from chub.cli.client import Client
from chub.daemon import main as chubd_main


async def test_wrapper_registers_and_receives_inject(
    chub_home: Path,
    fakeclaude_bin: Path,
    tmp_path: Path,
) -> None:
    stop = asyncio.Event()
    server_task = asyncio.create_task(chubd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists(), "chubd never created its socket"

    proc: asyncio.subprocess.Process | None = None
    fc_log = tmp_path / "fc.log"
    try:
        proc = await asyncio.create_subprocess_exec(
            "uv",
            "run",
            "chub-claude",
            "--name",
            "x",
            "--cwd",
            str(tmp_path),
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env={
                **os.environ,
                "CHUB_HOME": str(chub_home),
                "FAKECLAUDE_LOG": str(fc_log),
                "PATH": os.environ["PATH"],
            },
        )

        client = Client(sock)
        sid: str | None = None
        for _ in range(100):
            r = await client.call("list_sessions", {})
            if r["sessions"]:
                sid = r["sessions"][0]["id"]
                break
            await asyncio.sleep(0.1)
        assert sid is not None, "session never registered"

        await client.call(
            "inject",
            {
                "session_id": sid,
                "payload_b64": base64.b64encode(b"hello\n").decode(),
            },
        )
        await client.close()

        for _ in range(100):
            if fc_log.exists() and "hello" in fc_log.read_text():
                break
            await asyncio.sleep(0.1)
        assert fc_log.exists(), "fakeclaude log never created"
        assert "hello" in fc_log.read_text()
    finally:
        if proc is not None:
            try:
                proc.kill()
            except ProcessLookupError:
                pass
            try:
                await asyncio.wait_for(proc.wait(), timeout=3.0)
            except TimeoutError:
                pass
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
