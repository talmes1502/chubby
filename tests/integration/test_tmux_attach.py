"""Integration test for tmux attach: inject_tmux + capture-pane."""

from __future__ import annotations

import asyncio
import shutil
import sys
from pathlib import Path

import pytest

from chubby.daemon.attach.tmux import _capture_pane, inject_tmux
from chubby.daemon.session import Session, SessionKind, SessionStatus

_tmux = shutil.which("tmux")
pytestmark = pytest.mark.skipif(_tmux is None, reason="tmux not installed")


async def test_capture_pane_and_send_keys(tmp_path: Path) -> None:
    assert _tmux is not None  # narrowed by skipif
    sess = "chubtest_phase11"
    # Use asyncio.create_subprocess_exec to avoid ASYNC221 (no sync subprocess
    # in async functions) — the pre-test cleanup runs and is allowed to fail.
    pre = await asyncio.create_subprocess_exec(
        _tmux, "kill-session", "-t", sess, stderr=asyncio.subprocess.DEVNULL
    )
    await pre.wait()
    cmd = (
        f'{sys.executable} -c "import sys\n'
        "for line in sys.stdin: print(\\\"echo:\\\", line.strip(), flush=True)\""
    )
    new = await asyncio.create_subprocess_exec(
        _tmux, "new-session", "-d", "-s", sess, cmd
    )
    rc = await new.wait()
    assert rc == 0, "tmux new-session failed"
    target = f"{sess}:0.0"
    try:
        s = Session(
            id="s_t",
            hub_run_id="hr_t",
            name="t",
            color="#abcdef",
            kind=SessionKind.TMUX_ATTACHED,
            cwd=str(tmp_path),
            created_at=1,
            last_activity_at=1,
            status=SessionStatus.IDLE,
            tmux_target=target,
        )
        await inject_tmux(s, b"hello\n")
        await asyncio.sleep(0.5)
        out = await _capture_pane(target)
        assert "echo: hello" in out
    finally:
        kill = await asyncio.create_subprocess_exec(
            _tmux, "kill-session", "-t", sess, stderr=asyncio.subprocess.DEVNULL
        )
        await kill.wait()
