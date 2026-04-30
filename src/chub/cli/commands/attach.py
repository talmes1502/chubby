"""``chub attach`` — discover running ``claude`` processes and attach to them.

Modes:
- ``chub attach --list``               : print candidates as JSON
- ``chub attach --pick``               : interactive picker
- ``chub attach --all``                : attach to every tmux-attachable candidate
- ``chub attach --as-readonly``        : bulk-register every promote_required
                                          candidate as a readonly session
- ``chub attach tmux:session:0.0``     : attach to a specific tmux pane
"""

from __future__ import annotations

import asyncio
import json
import os
from typing import Any

import typer

from chub.cli.client import Client
from chub.daemon import paths


def run(
    target: str | None = typer.Argument(
        None, help="tmux:session:window.pane, or omit with --pick"
    ),
    pick: bool = typer.Option(False, "--pick"),
    list_: bool = typer.Option(False, "--list", "-l"),
    name: str | None = typer.Option(None, "--name"),
    all_: bool = typer.Option(False, "--all"),
    as_readonly: bool = typer.Option(
        False,
        "--as-readonly",
        help="register every promote_required candidate as a readonly session",
    ),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            scan = await c.call("scan_candidates", {})
            cs: list[dict[str, Any]] = list(scan["candidates"])
            cs = [x for x in cs if not x.get("already_attached")]
            if list_:
                typer.echo(json.dumps(cs, indent=2))
                return
            if as_readonly:
                ro_cs = [x for x in cs if x["classification"] == "promote_required"]
                if not ro_cs:
                    typer.echo("no promote_required candidates")
                    return
                for x in ro_cs:
                    nm = name or f"{os.path.basename(x['cwd'].rstrip('/'))}-{x['pid']}"
                    r = await c.call(
                        "attach_existing_readonly",
                        {"pid": x["pid"], "cwd": x["cwd"], "name": nm},
                    )
                    s = r["session"]
                    cs_id = s.get("claude_session_id")
                    suffix = f" (transcript={cs_id})" if cs_id else " (no transcript)"
                    typer.echo(f"attached readonly {s['name']} pid={x['pid']}{suffix}")
                return
            if pick or all_:
                tmux_cs = [x for x in cs if x["classification"] == "tmux_full"]
                if not tmux_cs:
                    typer.echo("no tmux-attachable candidates")
                    return
                if all_:
                    chosen = tmux_cs
                else:
                    for i, x in enumerate(cs):
                        glyph = "ok" if x["classification"] == "tmux_full" else "!"
                        typer.echo(
                            f"  [{i + 1}] claude pid {x['pid']:>6} cwd {x['cwd']} "
                            f"{glyph} {x['tmux_target'] or 'bare'}"
                        )
                    pick_idx = typer.prompt("pick", type=int)
                    chosen = [cs[pick_idx - 1]]
                for x in chosen:
                    nm = name or f"{os.path.basename(x['cwd'])}-{x['pid']}"
                    if x["classification"] != "tmux_full":
                        typer.echo(
                            f"  ! {nm}: not in tmux (use 'chub promote {nm}')"
                        )
                        continue
                    await c.call(
                        "attach_tmux",
                        {
                            "name": nm,
                            "cwd": x["cwd"],
                            "pid": x["pid"],
                            "tmux_target": x["tmux_target"],
                            "tags": [],
                        },
                    )
                    typer.echo(f"attached {nm} (tmux:{x['tmux_target']})")
                return
            if target is None:
                raise typer.BadParameter("provide a target or --pick/--list")
            if not target.startswith("tmux:"):
                raise typer.BadParameter("target must be tmux:session:window.pane")
            tmux_target = target.removeprefix("tmux:")
            match = next(
                (x for x in cs if x["tmux_target"] == tmux_target), None
            )
            if match is None:
                raise typer.BadParameter(
                    f"no candidate with tmux_target {tmux_target}"
                )
            nm = name or f"{os.path.basename(match['cwd'])}-{match['pid']}"
            await c.call(
                "attach_tmux",
                {
                    "name": nm,
                    "cwd": match["cwd"],
                    "pid": match["pid"],
                    "tmux_target": tmux_target,
                    "tags": [],
                },
            )
            typer.echo(f"attached {nm}")
        finally:
            await c.close()

    asyncio.run(go())
