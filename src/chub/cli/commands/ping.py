"""`chub ping` — round-trip RPC + show server time."""

from __future__ import annotations

import asyncio

import typer

from chub.cli.client import Client
from chub.daemon import paths


def run(message: str = typer.Argument("hi")) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            r = await c.call("ping", {"message": message})
            typer.echo(f"ok server_time_ms={r['server_time_ms']} echo={r.get('echo')!r}")
        finally:
            await c.close()

    asyncio.run(go())
