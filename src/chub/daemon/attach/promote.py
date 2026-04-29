"""Promote-attach: wait for raw claude PID to exit, then spawn chub-claude.

Used when an existing ``claude`` process can't be tmux-attached (e.g., it
isn't running inside a tmux pane). The user is asked to exit Claude in its
own terminal; once the PID dies, the daemon relaunches it under
``chub-claude`` so it joins the registry as a wrapped session.
"""

from __future__ import annotations

import asyncio
import os
import sys

from chub.daemon import paths


async def wait_for_exit(pid: int, *, timeout: float = 600.0) -> bool:  # noqa: ASYNC109
    """Poll ``kill(pid, 0)`` every 0.5s until the pid is dead or timeout fires.

    Returns ``True`` if the process exited, ``False`` if the timeout fired.
    """
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        except PermissionError:
            # Process exists but we can't signal it; treat as alive.
            pass
        await asyncio.sleep(0.5)
    return False


async def relaunch_wrapper(*, name: str, cwd: str, tags: list[str]) -> int:
    """Spawn a detached ``chub-claude`` wrapper that will register itself."""
    proc = await asyncio.create_subprocess_exec(
        sys.executable,
        "-m",
        "chub.wrapper.main",
        "--name",
        name,
        "--cwd",
        cwd,
        "--tags",
        ",".join(tags),
        stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.DEVNULL,
        stderr=asyncio.subprocess.DEVNULL,
        env={
            **os.environ,
            "CHUB_NAME": name,
            "CHUB_HOME": str(paths.hub_home()),
        },
        start_new_session=True,
    )
    return proc.pid
