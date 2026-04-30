"""`chubby purge` — delete a run's logs/FTS or a single session's FTS rows."""

from __future__ import annotations

import asyncio
import shutil

import typer

from chubby.cli.client import Client
from chubby.daemon import paths


def run(
    run_id: str | None = typer.Option(None, "--run", help="hub-run id to purge"),
    session: str | None = typer.Option(None, "--session", help="session name to purge"),
    yes: bool = typer.Option(False, "--yes", help="skip confirmation prompt"),
) -> None:
    if run_id is None and session is None:
        raise typer.BadParameter("specify --run or --session")
    if run_id is not None and session is not None:
        raise typer.BadParameter("--run and --session are mutually exclusive")
    if not yes:
        what = f"run {run_id}" if run_id else f"session {session}"
        typer.confirm(
            f"permanently delete logs and FTS entries for {what}?", abort=True
        )

    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            await c.call("purge", {"run_id": run_id, "session": session})
            if run_id is not None:
                run_dir = paths.runs_dir() / run_id
                if run_dir.exists():
                    shutil.rmtree(run_dir)
            typer.echo("purged")
        finally:
            await c.close()

    asyncio.run(go())
