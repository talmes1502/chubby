"""Tmux-based attach: capture-pane polling for output, send-keys for inject."""

from __future__ import annotations

import asyncio
import logging
from typing import TYPE_CHECKING

from chubby.proto.errors import ChubError, ErrorCode

if TYPE_CHECKING:
    from chubby.daemon.registry import Registry
    from chubby.daemon.session import Session

log = logging.getLogger(__name__)


async def _capture_pane(target: str) -> str:
    proc = await asyncio.create_subprocess_exec(
        "tmux",
        "capture-pane",
        "-p",
        "-e",
        "-S",
        "-1000",
        "-t",
        target,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.DEVNULL,
    )
    out, _ = await proc.communicate()
    return out.decode("utf-8", errors="replace")


async def watch_pane(
    registry: Registry, session_id: str, target: str, *, stop: asyncio.Event
) -> None:
    """Poll capture-pane at ~5Hz; emit diffs as output_chunk events into registry."""
    last = ""
    while not stop.is_set():
        try:
            cur = await _capture_pane(target)
        except FileNotFoundError:
            log.error("tmux not found")
            return
        if cur != last:
            # naive diff: send only the suffix that changed
            common = 0
            for i, (a, b) in enumerate(zip(last, cur, strict=False)):
                if a != b:
                    break
                common = i + 1
            new = cur[common:]
            if new:
                await registry.record_chunk(session_id, new.encode(), role="raw")
            last = cur
        try:
            await asyncio.wait_for(stop.wait(), timeout=0.2)
        except TimeoutError:
            pass


async def inject_tmux(session: Session, payload: bytes) -> None:
    """Send payload to a tmux pane via tmux send-keys."""
    target = session.tmux_target
    if target is None:
        raise ChubError(ErrorCode.TMUX_TARGET_INVALID, "session has no tmux_target")
    text = payload.decode("utf-8", errors="replace")
    commit = True
    if text.endswith("\n") or text.endswith("\r"):
        text = text.rstrip("\n\r")
    proc = await asyncio.create_subprocess_exec(
        "tmux",
        "send-keys",
        "-l",
        "-t",
        target,
        text,
        stdout=asyncio.subprocess.DEVNULL,
        stderr=asyncio.subprocess.PIPE,
    )
    _, err = await proc.communicate()
    if proc.returncode != 0:
        raise ChubError(
            ErrorCode.TMUX_TARGET_INVALID,
            f"tmux send-keys failed: {err.decode(errors='replace').strip()}",
        )
    if commit:
        proc2 = await asyncio.create_subprocess_exec(
            "tmux",
            "send-keys",
            "-t",
            target,
            "Enter",
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL,
        )
        await proc2.wait()
