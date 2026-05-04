"""`chubby list` — show all sessions in the current hub-run."""

from __future__ import annotations

import asyncio
from typing import Any

from chubby.cli.client import Client
from chubby.cli.format import prefix
from chubby.cli.output import OUT
from chubby.daemon import paths

_STATUS_GLYPH = {
    "idle": "○",
    "thinking": "●",
    "awaiting_user": "⚡",
    "dead": "✕",
}


def _pretty_session(s: dict[str, Any]) -> str:
    glyph = _STATUS_GLYPH.get(s["status"], "?")
    return f"{prefix(s['name'], s['color'])} {glyph} {s['kind']:14s} {s['cwd']}"


def run() -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            r = await c.call("list_sessions", {})
            sessions = r.get("sessions", [])
            OUT.list(sessions, pretty_line=_pretty_session, empty_message="(no sessions)")
        finally:
            await c.close()

    asyncio.run(go())
