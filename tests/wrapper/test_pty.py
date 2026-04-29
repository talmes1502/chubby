"""Tests for the PtySession PTY wrapper."""

from __future__ import annotations

import asyncio
import sys
from pathlib import Path

from chub.wrapper.pty import PtySession


async def test_spawn_echoes_stdin(tmp_path: Path) -> None:
    log = tmp_path / "fc.log"
    s = PtySession(
        [
            sys.executable,
            "-c",
            "import sys\n"
            "print('READY', flush=True)\n"
            "for line in sys.stdin:\n"
            "    print('echo:', line.rstrip(), flush=True)\n",
        ],
        cwd=str(tmp_path),
        env={"FAKECLAUDE_LOG": str(log)},
    )
    await s.start()
    received: list[bytes] = []

    async def reader() -> None:
        async for chunk in s.iter_output():
            received.append(chunk)
            if b"echo: hi" in b"".join(received):
                return

    task = asyncio.create_task(reader())
    await asyncio.sleep(0.2)
    await s.write_user(b"hi\n")
    try:
        await asyncio.wait_for(task, timeout=3.0)
    finally:
        await s.terminate()
    blob = b"".join(received)
    assert b"READY" in blob
    assert b"echo: hi" in blob
