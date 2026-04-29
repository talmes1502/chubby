"""`chub rename` — rename a session by its current name."""

from __future__ import annotations

import asyncio

import typer

from chub.cli.client import Client
from chub.daemon import paths


def run(
    old_name: str = typer.Argument(...),
    new_name: str = typer.Argument(...),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sessions = (await c.call("list_sessions", {})).get("sessions", [])
            match = next((s for s in sessions if s["name"] == old_name), None)
            if match is None:
                raise typer.BadParameter(f"no session named {old_name!r}")
            await c.call("rename_session", {"id": match["id"], "name": new_name})
            typer.echo(f"renamed {old_name} -> {new_name}")
        finally:
            await c.close()

    asyncio.run(go())
