"""`chubby down` — stop the running chubbyd via PID file."""

from __future__ import annotations

import os
import signal

import typer

from chubby.daemon import paths


def run() -> None:
    p = paths.pid_path()
    if not p.exists():
        typer.echo("chubbyd is not running")
        raise typer.Exit(0)
    pid = int(p.read_text().strip())
    try:
        os.kill(pid, signal.SIGTERM)
        typer.echo(f"sent SIGTERM to chubbyd (pid {pid})")
    except ProcessLookupError:
        typer.echo(f"chubbyd pidfile points to dead pid {pid}; removing")
        p.unlink(missing_ok=True)
