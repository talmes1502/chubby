"""``chub send`` — inject a payload into a running session by name."""

from __future__ import annotations

import asyncio
import base64

import typer

from chub.cli.client import Client
from chub.daemon import paths


def run(
    name: str = typer.Argument(...),
    payload: str = typer.Argument(...),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sessions = (await c.call("list_sessions", {}))["sessions"]
            match = next((s for s in sessions if s["name"] == name), None)
            if match is None:
                raise typer.BadParameter(f"no session named {name!r}")
            await c.call(
                "inject",
                {
                    "session_id": match["id"],
                    "payload_b64": base64.b64encode(payload.encode()).decode(),
                },
            )
            typer.echo(f"sent to {name}")
        finally:
            await c.close()

    asyncio.run(go())
