"""`chubby up` — start chubbyd in the foreground (or detached with --detach)."""

from __future__ import annotations

import asyncio
import os
import subprocess
import sys

import typer


def run(
    detach: bool = typer.Option(False, "--detach", help="Run chubbyd in the background"),
    resume: str | None = typer.Option(
        None, "--resume", help="Resume a prior hub-run id (or 'last')"
    ),
) -> None:
    if detach:
        env = os.environ.copy()
        if resume:
            env["CHUBBY_RESUME"] = resume
        proc = subprocess.Popen(
            [sys.executable, "-m", "chubby.daemon.main"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
            env=env,
        )
        typer.echo(f"chubbyd started (pid {proc.pid})")
        return
    from chubby.daemon.main import serve

    asyncio.run(serve())
