"""``chubby detach`` — release a tmux-attached session and mark it dead.

Stops the daemon's pane-watcher and updates session status to DEAD. The
underlying ``claude`` process keeps running in its tmux pane.
"""

from __future__ import annotations

import asyncio

import typer

from chubby.cli.client import Client
from chubby.daemon import paths


def run(name: str = typer.Argument(...)) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sessions = (await c.call("list_sessions", {}))["sessions"]
            match = next((s for s in sessions if s["name"] == name), None)
            if match is None:
                raise typer.BadParameter(f"no session named {name!r}")
            await c.call("detach_session", {"id": match["id"]})
            typer.echo(f"detached {name}")
        finally:
            await c.close()

    asyncio.run(go())
