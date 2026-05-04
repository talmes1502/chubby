"""``chubby grep`` — full-text search transcripts across sessions."""

from __future__ import annotations

import asyncio

import typer

from chubby.cli.client import Client
from chubby.daemon import paths


def run(
    query: str = typer.Argument(...),
    session: str | None = typer.Option(None, "--session"),
    all_runs: bool = typer.Option(False, "--all-runs"),
    run_id: str | None = typer.Option(None, "--run"),
    limit: int = typer.Option(200, "--limit"),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sid: str | None = None
            if session is not None:
                sessions = (await c.call("list_sessions", {}))["sessions"]
                match = next((s for s in sessions if s["name"] == session), None)
                if match is None:
                    raise typer.BadParameter(f"no session named {session!r}")
                sid = match["id"]
            r = await c.call(
                "search_transcripts",
                {
                    "query": query,
                    "session_id": sid,
                    "hub_run_id": run_id,
                    "all_runs": all_runs,
                    "limit": limit,
                },
            )
            for m in r["matches"]:
                typer.echo(f"[{m['session_id']} {m['hub_run_id']} {m['ts']}] {m['snippet']}")
        finally:
            await c.close()

    asyncio.run(go())
