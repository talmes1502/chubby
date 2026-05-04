"""``chubby preset`` — list/create/delete/show/apply spawn presets.

Presets are saved templates for ``chubby spawn``. ``apply`` resolves
the template (date / name substitutions, ``~`` expansion) and calls
``spawn_session`` with the resulting params, so any CLI/RPC override
flow flowing through ``spawn_session`` (worktree creation, lifecycle
scripts) Just Works without preset code knowing about them.

We deliberately store ``~``-prefixed paths verbatim on disk (so
presets are portable across machines / users) and expand at apply
time. Override ``--cwd`` on ``apply`` is also expanded so a one-shot
``chubby preset apply web --cwd ~/other`` works the same way.
"""

from __future__ import annotations

import asyncio
import os
from typing import Any

import typer

from chubby.cli.client import Client
from chubby.cli.output import OUT
from chubby.daemon import paths
from chubby.daemon import presets as presets_mod

app = typer.Typer(no_args_is_help=True, help="Manage spawn presets.")


@app.command("list")
def list_cmd() -> None:
    """List all saved presets."""
    rows = [p.to_dict() for p in presets_mod.load_presets()]

    def _pretty(p: dict[str, Any]) -> str:
        cwd = p.get("cwd") or ""
        branch = p.get("branch")
        bits = [p["name"]]
        if cwd:
            bits.append(f"cwd={cwd}")
        if branch:
            bits.append(f"branch={branch}")
        if p.get("tags"):
            bits.append(f"tags={','.join(p['tags'])}")
        if p.get("description"):
            bits.append(f"-- {p['description']}")
        return " ".join(bits)

    OUT.list(rows, pretty_line=_pretty, empty_message="(no presets)")


@app.command("create")
def create_cmd(
    name: str = typer.Argument(..., help="Preset name (templateable)."),
    cwd: str = typer.Option("", "--cwd"),
    branch: str | None = typer.Option(None, "--branch"),
    tags: str = typer.Option("", "--tags"),
    description: str = typer.Option("", "--description"),
) -> None:
    """Save a new preset (or replace one with the same name)."""
    p = presets_mod.Preset(
        name=name,
        cwd=cwd,
        branch=branch,
        tags=[t for t in tags.split(",") if t],
        description=description,
    )
    presets_mod.upsert_preset(p)
    OUT.object(p.to_dict(), pretty_line=lambda d: f"saved preset {d['name']}")


@app.command("delete")
def delete_cmd(
    name: str = typer.Argument(..., help="Preset to remove."),
) -> None:
    if not presets_mod.delete_preset(name):
        typer.echo(f"no preset named {name!r}", err=True)
        raise typer.Exit(code=1)
    typer.echo(f"deleted preset {name}")


@app.command("show")
def show_cmd(
    name: str = typer.Argument(..., help="Preset to show."),
) -> None:
    p = presets_mod.get_preset(name)
    if p is None:
        typer.echo(f"no preset named {name!r}", err=True)
        raise typer.Exit(code=1)
    OUT.object(p.to_dict(), pretty_line=lambda d: _full_pretty(d))


def _full_pretty(d: dict[str, Any]) -> str:
    """Multi-line pretty print for ``show``."""
    lines = [f"preset: {d['name']}"]
    if d.get("description"):
        lines.append(f"  description: {d['description']}")
    if d.get("cwd"):
        lines.append(f"  cwd:         {d['cwd']}")
    if d.get("branch"):
        lines.append(f"  branch:      {d['branch']}")
    if d.get("tags"):
        lines.append(f"  tags:        {','.join(d['tags'])}")
    return "\n".join(lines)


@app.command("apply")
def apply_cmd(
    name: str = typer.Argument(..., help="Preset to apply."),
    override_name: str | None = typer.Option(
        None, "--name", help="Override the resolved session name."
    ),
    override_cwd: str | None = typer.Option(
        None, "--cwd", help="Override the resolved cwd."
    ),
    override_branch: str | None = typer.Option(
        None, "--branch", help="Override (or set) the branch."
    ),
) -> None:
    """Resolve a preset's templates and spawn a session from it."""
    p = presets_mod.get_preset(name)
    if p is None:
        typer.echo(f"no preset named {name!r}", err=True)
        raise typer.Exit(code=1)
    overrides: dict[str, Any] = {}
    if override_name is not None:
        overrides["name"] = override_name
    if override_cwd is not None:
        # Expand ``~`` at apply time so an override like
        # ``--cwd ~/other`` lands on the right absolute path.
        overrides["cwd"] = os.path.expanduser(override_cwd)
    if override_branch is not None:
        overrides["branch"] = override_branch
    params = p.render(overrides=overrides)

    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            r = await c.call("spawn_session", params)
            OUT.object(
                r, pretty_line=lambda _: f"spawned {r['session']['id']} ({params['name']})"
            )
        finally:
            await c.close()

    asyncio.run(go())
