"""``chubby promote`` — promote a readonly session to a wrapped one.

Asks the daemon to wait for the raw ``claude`` PID to exit, then relaunch the
session under ``chubby-claude``. Blocks until the daemon completes the swap.
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
            match = next(
                (
                    s
                    for s in sessions
                    if s["name"] == name and s["kind"] == "readonly"
                ),
                None,
            )
            if match is None:
                raise typer.BadParameter(f"no readonly session named {name!r}")
            typer.echo(
                "Exit Claude in its terminal (Ctrl+D or /exit). "
                "Waiting for pid to die..."
            )
            await c.call("promote_session", {"id": match["id"]})
            typer.echo(f"promoted {name}")
        finally:
            await c.close()

    asyncio.run(go())
