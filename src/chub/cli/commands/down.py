"""`chub down` — stop the running chubd via PID file."""

from __future__ import annotations

import os
import signal

import typer

from chub.daemon import paths


def run() -> None:
    p = paths.pid_path()
    if not p.exists():
        typer.echo("chubd is not running")
        raise typer.Exit(0)
    pid = int(p.read_text().strip())
    try:
        os.kill(pid, signal.SIGTERM)
        typer.echo(f"sent SIGTERM to chubd (pid {pid})")
    except ProcessLookupError:
        typer.echo(f"chubd pidfile points to dead pid {pid}; removing")
        p.unlink(missing_ok=True)
