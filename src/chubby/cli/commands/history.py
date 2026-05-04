"""`chubby history` — list past hub-runs or inspect a single one."""

from __future__ import annotations

import asyncio
from datetime import datetime
from typing import Any

import typer

from chubby.cli.client import Client
from chubby.cli.output import OUT
from chubby.daemon import paths


def _pretty_run(r: dict[str, Any]) -> str:
    started = datetime.fromtimestamp(r["started_at"] / 1000).strftime(
        "%Y-%m-%d %H:%M"
    )
    ended = (
        datetime.fromtimestamp(r["ended_at"] / 1000).strftime("%H:%M")
        if r["ended_at"]
        else "running"
    )
    note = f'  "{r["notes"]}"' if r.get("notes") else ""
    return f"{r['id']}  {started} -> {ended}{note}"


def _pretty_match(m: dict[str, Any]) -> str:
    return f"{m['ts']}  {m['session_id']}  {m['snippet']}"


def run(
    run_id: str | None = typer.Argument(None),
    tail: str | None = typer.Option(
        None, "--tail", help="Tail one session's log (by name)"
    ),
    search: str | None = typer.Option(None, "--search"),
) -> None:
    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            if run_id is None:
                runs = (await c.call("list_hub_runs", {}))["runs"]
                OUT.list(runs, pretty_line=_pretty_run, empty_message="(no hub runs)")
                return
            if search is not None:
                matches = (
                    await c.call(
                        "search_transcripts",
                        {"query": search, "hub_run_id": run_id},
                    )
                )["matches"]
                OUT.list(matches, pretty_line=_pretty_match, empty_message="(no matches)")
                return
            r = await c.call("get_hub_run", {"id": run_id})
            run, sessions = r["run"], r["sessions"]
            # The hub-run detail view is a one-shot "show me everything
            # about this run" inspection — leave it pretty-only for now;
            # JSON callers can call list_hub_runs/get_hub_run via a
            # script if they need the raw shape. Tail mode is also kept
            # plain — log files are inherently text streams.
            typer.echo(f"{run['id']}  notes={run.get('notes') or ''}")
            for s in sessions:
                typer.echo(
                    f"  {s['name']:20s} {s['kind']:14s} {s['status']:14s} {s['cwd']}"
                )
            if tail:
                logfile = paths.runs_dir() / run_id / "logs" / f"{tail}.log"
                if logfile.exists():
                    typer.echo(logfile.read_text())
                else:
                    typer.echo(f"(no log file at {logfile})")
        finally:
            await c.close()

    asyncio.run(go())
