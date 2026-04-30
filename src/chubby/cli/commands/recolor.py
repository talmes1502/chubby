"""`chubby recolor` — set a session's color to a hex value."""

from __future__ import annotations

import asyncio
import re

import typer

from chubby.cli.client import Client
from chubby.daemon import paths

_HEX = re.compile(r"^#[0-9a-fA-F]{6}$")


def run(
    name: str = typer.Argument(...),
    color: str = typer.Argument(...),
) -> None:
    if not _HEX.match(color):
        raise typer.BadParameter("color must be #RRGGBB")

    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sessions = (await c.call("list_sessions", {})).get("sessions", [])
            match = next((s for s in sessions if s["name"] == name), None)
            if match is None:
                raise typer.BadParameter(f"no session named {name!r}")
            await c.call("recolor_session", {"id": match["id"], "color": color})
            typer.echo(f"recolored {name} -> {color}")
        finally:
            await c.close()

    asyncio.run(go())
