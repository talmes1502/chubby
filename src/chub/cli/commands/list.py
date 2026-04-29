"""`chub list` — show all sessions in the current hub-run."""

from __future__ import annotations

import asyncio

import typer

from chub.cli.client import Client
from chub.cli.format import prefix
from chub.daemon import paths

_STATUS_GLYPH = {
    "idle": "○",
    "thinking": "●",
    "awaiting_user": "⚡",
    "dead": "✕",
}


def run() -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            r = await c.call("list_sessions", {})
            sessions = r.get("sessions", [])
            if not sessions:
                typer.echo("(no sessions)")
                return
            for s in sessions:
                glyph = _STATUS_GLYPH.get(s["status"], "?")
                typer.echo(
                    f"{prefix(s['name'], s['color'])} {glyph} {s['kind']:14s} {s['cwd']}"
                )
        finally:
            await c.close()

    asyncio.run(go())
