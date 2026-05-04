"""``chubby register-readonly`` — invoked by the SessionStart hook.

The session id and cwd can come from CLI flags (manual invocation /
legacy hook config) or from the Claude Code hook payload piped on
stdin. Silently no-ops when the daemon isn't running so it never
blocks Claude startup.
"""

from __future__ import annotations

import asyncio
import contextlib
import os

import typer

from chubby.cli.client import Client
from chubby.cli.commands._hook_input import read_hook_payload
from chubby.daemon import paths
from chubby.proto.errors import ChubError


def run(
    claude_session_id: str | None = typer.Option(None, "--claude-session-id"),
    cwd: str | None = typer.Option(None, "--cwd"),
    name: str | None = typer.Option(None, "--name"),
) -> None:
    if not claude_session_id or not cwd:
        payload = read_hook_payload()
        if not claude_session_id:
            sid = payload.get("session_id")
            if isinstance(sid, str) and sid:
                claude_session_id = sid
        if not cwd:
            payload_cwd = payload.get("cwd")
            if isinstance(payload_cwd, str) and payload_cwd:
                cwd = payload_cwd
    if not cwd:
        cwd = os.getcwd()
    if not claude_session_id:
        return

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
