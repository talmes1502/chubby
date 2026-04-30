"""`chubby diag <name>` — print captured wrapper stderr for a session.

Daemon-spawned wrappers redirect stdout+stderr to
``<run.dir>/wrappers/<safe-name>.stderr``. This command finds that file by
session name, prints it, and is the recommended first step when a session's
viewport is blank or its claude child died unexpectedly.

Sessions that were registered outside of ``chubby spawn`` (raw user-launched
``chubby-claude`` etc.) won't have a diagnostic file — we tell the user that
explicitly rather than crashing.
"""

from __future__ import annotations

import asyncio
import re

import typer

from chubby.cli.client import Client
from chubby.daemon import paths


def run(
    name: str = typer.Argument(...),
    tail: int = typer.Option(
        0, "--tail", "-n", help="Show only the last N lines"
    ),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            r = await c.call("list_sessions", {})
        finally:
            await c.close()
        sessions = r.get("sessions", [])

        match = next((s for s in sessions if s["name"] == name), None)
        if match is None:
            raise typer.BadParameter(f"no session named {name!r}")

        run_id = match["hub_run_id"]
        safe_name = re.sub(r"[^a-zA-Z0-9_-]", "_", name)
        log_path = paths.runs_dir() / run_id / "wrappers" / f"{safe_name}.stderr"

        if not log_path.exists():
            typer.echo(f"(no diagnostic log at {log_path})")
            typer.echo(
                "this session was probably wrapper-launched outside `chubby spawn` "
                "(e.g., user-launched chubby-claude). diagnostics only capture "
                "spawn_session-launched wrappers."
            )
            raise typer.Exit(0)

        content = log_path.read_text(errors="replace")
        if tail > 0:
            content = "\n".join(content.splitlines()[-tail:])
        typer.echo(f"=== {log_path} ===")
        typer.echo(content)

    asyncio.run(go())
