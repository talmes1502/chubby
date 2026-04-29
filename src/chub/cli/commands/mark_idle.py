"""``chub mark-idle`` — invoked by the Stop hook.

Silently no-ops when the daemon isn't running.
"""

from __future__ import annotations

import asyncio
import contextlib

import typer

from chub.cli.client import Client
from chub.daemon import paths
from chub.proto.errors import ChubError


def run(
    claude_session_id: str = typer.Option(..., "--claude-session-id"),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            await c.call("mark_idle", {"claude_session_id": claude_session_id})
        except (FileNotFoundError, ConnectionRefusedError, ChubError):
            pass
        finally:
            with contextlib.suppress(Exception):
                await c.close()

    asyncio.run(go())
