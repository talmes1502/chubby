"""`chubby broadcast` — inject the same payload into many sessions at once.

Filters by ``--only`` (names) and/or ``--tag`` (tags). Dead and read-only
sessions are excluded. Without ``--yes`` the user is prompted for
confirmation.
"""

from __future__ import annotations

import asyncio
import base64

import typer

from chubby.cli.client import Client
from chubby.daemon import paths
from chubby.proto.errors import ChubError


def run(
    payload: str = typer.Argument(...),
    only: list[str] = typer.Option(  # noqa: B008
        None, "--only", help="Limit to these session names (repeatable)."
    ),
    tag: list[str] = typer.Option(  # noqa: B008
        None, "--tag", help="Limit to sessions carrying this tag (repeatable)."
    ),
    confirm: bool = typer.Option(
        False, "--yes", help="Skip the confirmation prompt."
    ),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sessions = (await c.call("list_sessions", {})).get("sessions", [])
            targets = [
                s for s in sessions
                if s["status"] != "dead" and s["kind"] != "readonly"
            ]
            if only:
                only_set = set(only)
                targets = [s for s in targets if s["name"] in only_set]
            if tag:
                tagset = set(tag)
                targets = [s for s in targets if tagset.intersection(s["tags"])]
            if not targets:
                typer.echo("no targets")
                return
            if not confirm:
                names = ", ".join(s["name"] for s in targets)
                typer.confirm(
                    f"broadcast to {len(targets)} sessions ({names})?",
                    abort=True,
                )
            payload_b64 = base64.b64encode(payload.encode()).decode()
            sent = 0
            failed: list[tuple[str, str]] = []
            for s in targets:
                try:
                    await c.call(
                        "inject",
                        {
                            "session_id": s["id"],
                            "payload_b64": payload_b64,
                        },
                    )
                    sent += 1
                except ChubError as e:
                    failed.append((s["name"], str(e)))
            typer.echo(f"broadcast: {sent} sent, {len(failed)} failed")
            for name, err in failed:
                typer.echo(f"  x {name}: {err}")
        finally:
            await c.close()

    asyncio.run(go())
