"""``chub register-readonly`` — invoked by the SessionStart hook.

Silently no-ops when the daemon isn't running so it never blocks Claude
startup. Hook scripts call this in the background.
"""

from __future__ import annotations

import asyncio
import contextlib
import os

import typer

from chub.cli.client import Client
from chub.daemon import paths
from chub.proto.errors import ChubError


def run(
    claude_session_id: str = typer.Option(..., "--claude-session-id"),
    cwd: str = typer.Option(os.getcwd(), "--cwd"),
    name: str | None = typer.Option(None, "--name"),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            await c.call(
                "register_readonly",
                {
                    "claude_session_id": claude_session_id,
                    "cwd": cwd,
                    "name": name,
                    "tags": [],
                },
            )
        except (FileNotFoundError, ConnectionRefusedError, ChubError):
            # Daemon not running or rejected the call — silently no-op so the
            # SessionStart hook never blocks Claude.
            pass
        finally:
            with contextlib.suppress(Exception):
                await c.close()

    asyncio.run(go())
