"""`chubby tag` — add/remove tags on a session by name.

Syntax: ``chubby tag <name> +foo -bar +baz``. Each argument must start with
``+`` (add) or ``-`` (remove). Anything else is rejected.
"""

from __future__ import annotations

import asyncio

import typer

from chubby.cli.client import Client
from chubby.daemon import paths


def run(
    name: str = typer.Argument(...),
    tag_args: list[str] = typer.Argument(...),  # noqa: B008
) -> None:
    add: list[str] = []
    remove: list[str] = []
    for a in tag_args:
        if a.startswith("+"):
            add.append(a[1:])
        elif a.startswith("-"):
            remove.append(a[1:])
        else:
            raise typer.BadParameter(f"tag must start with + or -: {a!r}")

    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sessions = (await c.call("list_sessions", {})).get("sessions", [])
            match = next((s for s in sessions if s["name"] == name), None)
            if match is None:
                raise typer.BadParameter(f"no session named {name!r}")
            await c.call(
                "set_session_tags",
                {"id": match["id"], "add": add, "remove": remove},
            )
            typer.echo(f"tagged {name}: +{add} -{remove}")
        finally:
            await c.close()

    asyncio.run(go())
