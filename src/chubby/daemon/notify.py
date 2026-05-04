"""Cross-platform OS notifier.

macOS uses ``osascript`` (display notification …); Linux uses
``notify-send``. On other platforms ``notify`` is a no-op. Failures
(missing binary, slow response) are swallowed with a warning so the
daemon never blocks on the notifier.

Setting ``CHUBBY_NO_NOTIFY=1`` (or the legacy ``CHUB_NO_NOTIFY=1``)
disables notifications entirely — useful when iterating on chubby
itself so every status flip doesn't pop a desktop banner.
"""

from __future__ import annotations

import asyncio
import logging
import os
import sys

log = logging.getLogger(__name__)


async def notify(title: str, body: str) -> None:
    if os.environ.get("CHUBBY_NO_NOTIFY") or os.environ.get("CHUB_NO_NOTIFY"):
        return
    if sys.platform == "darwin":
        cmd = [
            "osascript",
            "-e",
            f'display notification "{_esc(body)}" with title "{_esc(title)}"',
        ]
    elif sys.platform.startswith("linux"):
        cmd = ["notify-send", title, body]
    else:
        return
    try:
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL,
        )
        await asyncio.wait_for(proc.wait(), timeout=2.0)
    except (FileNotFoundError, TimeoutError):
        log.warning("OS notifier not available: %s", cmd[0])


def _esc(s: str) -> str:
    return s.replace("\\", "\\\\").replace('"', '\\"')
