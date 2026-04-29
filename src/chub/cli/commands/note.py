"""`chub note` — attach a free-form note to a hub-run."""

from __future__ import annotations

import asyncio

import typer

from chub.cli.client import Client
from chub.daemon import paths


def run(
    run_id: str = typer.Argument(...),
    note: str = typer.Argument(...),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            await c.call("set_hub_run_note", {"id": run_id, "note": note})
            typer.echo(f"noted {run_id}")
        finally:
            await c.close()

    asyncio.run(go())
