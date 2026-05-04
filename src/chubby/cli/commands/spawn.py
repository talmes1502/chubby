"""``chubby spawn`` — ask the daemon to launch a fresh ``chubby-claude`` wrapper."""

from __future__ import annotations

import asyncio
import os
from typing import Any

import typer

from chubby.cli.client import Client
from chubby.cli.output import OUT
from chubby.daemon import paths


def run(
    name: str = typer.Option(..., "--name"),
    cwd: str = typer.Option(os.getcwd(), "--cwd"),
    tags: str = typer.Option("", "--tags"),
    branch: str | None = typer.Option(
        None,
        "--branch",
        help="Spawn into a fresh git worktree on this branch "
        "(creates the branch if it doesn't exist)",
    ),
    pr: int | None = typer.Option(
        None,
        "--pr",
        help="Resolve a GitHub PR via `gh pr view` and spawn into a "
        "worktree on its head branch (best-effort; requires gh)",
    ),
) -> None:
    # Expand ``~`` ourselves — typer leaves it literal when the
    # value arrives quoted or from a config file. The shell normally
    # handles the unquoted case, but the daemon-side fallback in
    # spawn_session catches anything that slips through.
    cwd = os.path.expanduser(cwd)

    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            params: dict[str, Any] = {
                "name": name,
                "cwd": cwd,
                "tags": [t for t in tags.split(",") if t],
            }
            if branch is not None:
                params["branch"] = branch
            if pr is not None:
                params["pr"] = pr
            r = await c.call("spawn_session", params)
            # Pretty: "spawned <id> (<name>)"; quiet: just the id;
            # json: the full session record. OUT.object handles the
            # session-wrapper extraction for the QUIET id path.
            def _pretty(_obj: dict[str, Any]) -> str:
                return f"spawned {r['session']['id']} ({name})"

            OUT.object(r, pretty_line=_pretty)
        finally:
            await c.close()

    asyncio.run(go())
