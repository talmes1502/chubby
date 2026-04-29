"""``chub spawn`` — ask the daemon to launch a fresh ``chub-claude`` wrapper."""

from __future__ import annotations

import asyncio
import os

import typer

from chub.cli.client import Client
from chub.daemon import paths


def run(
    name: str = typer.Option(..., "--name"),
    cwd: str = typer.Option(os.getcwd(), "--cwd"),
    tags: str = typer.Option("", "--tags"),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            r = await c.call(
                "spawn_session",
                {
                    "name": name,
                    "cwd": cwd,
                    "tags": [t for t in tags.split(",") if t],
                },
            )
            typer.echo(f"spawned {r['session']['id']} ({name})")
        finally:
            await c.close()

    asyncio.run(go())
