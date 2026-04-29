"""`chub respawn` — relaunch a dead session under its original name.

The dead session row still occupies the original name (we keep dead rows
for history). To respawn we ``spawn_session`` under a temporary
``-r``-suffixed name, then ``rename_session`` it back to the original.
The rename succeeds because dead sessions are excluded from the live
name set.
"""

from __future__ import annotations

import asyncio

import typer

from chub.cli.client import Client
from chub.daemon import paths


def run(name: str = typer.Argument(...)) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sessions = (await c.call("list_sessions", {}))["sessions"]
            match = next((s for s in sessions if s["name"] == name), None)
            if match is None:
                raise typer.BadParameter(f"no session named {name!r}")
            if match["status"] != "dead":
                raise typer.BadParameter(
                    f"session {name!r} is not dead (status={match['status']})"
                )
            r = await c.call(
                "spawn_session",
                {
                    "name": f"{name}-r",
                    "cwd": match["cwd"],
                    "tags": match["tags"],
                },
            )
            await c.call(
                "rename_session", {"id": r["session"]["id"], "name": name}
            )
            typer.echo(f"respawned {name}")
        finally:
            await c.close()

    asyncio.run(go())
