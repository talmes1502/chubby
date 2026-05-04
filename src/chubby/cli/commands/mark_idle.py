"""``chubby mark-idle`` — invoked by the Stop hook.

The session id can come from ``--claude-session-id`` (manual invocation
or legacy hook config) or from the Claude Code hook payload piped on
stdin (the format Claude Code actually uses for hook events). The
command is a silent no-op when neither path produces an id, so a
broken hook config never blocks Claude.
"""

from __future__ import annotations

import asyncio
import contextlib

import typer

from chubby.cli.client import Client
from chubby.cli.commands._hook_input import read_hook_payload
from chubby.daemon import paths
from chubby.proto.errors import ChubError


def run(
    claude_session_id: str | None = typer.Option(None, "--claude-session-id"),
) -> None:
    if not claude_session_id:
        payload = read_hook_payload()
        sid = payload.get("session_id")
        if isinstance(sid, str) and sid:
            claude_session_id = sid
    if not claude_session_id:
        return

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
