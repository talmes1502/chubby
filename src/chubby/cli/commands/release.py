"""``chubby release`` — full teardown for one or many sessions.

Single name (``chubby release web``) or bulk filters (``--all``,
``--tag``, ``--idle-since``). Calls ``release_session`` per match,
falling back to ``detach_session`` for sessions whose claude id
hasn't bound yet (release_session refuses those because it can't
return a resume tuple).

Output flips to ids-only under agent-context env vars so
``chubby list --quiet | xargs -L1 chubby release`` and similar
pipelines work.
"""

from __future__ import annotations

import asyncio
import json
import re
import time
from typing import Any

import typer

from chubby.cli.client import Client
from chubby.cli.output import OUT, Mode
from chubby.daemon import paths
from chubby.proto.errors import ChubError, ErrorCode

_DURATION_RE = re.compile(r"^(\d+)([smhd])$")
_DURATION_UNITS_MS: dict[str, int] = {
    "s": 1000,
    "m": 60 * 1000,
    "h": 60 * 60 * 1000,
    "d": 24 * 60 * 60 * 1000,
}


def _parse_duration_ms(spec: str) -> int:
    m = _DURATION_RE.match(spec.strip().lower())
    if not m:
        raise typer.BadParameter(
            f"--idle-since must be like '30s' / '15m' / '2h' / '1d', got {spec!r}"
        )
    n, unit = int(m.group(1)), m.group(2)
    return n * _DURATION_UNITS_MS[unit]


def run(
    names: list[str] = typer.Argument(  # noqa: B008
        None,
        metavar="[NAME...]",
        help="Session names to release. Mutually exclusive with --all/--tag/--idle-since.",
    ),
    all_: bool = typer.Option(
        False, "--all", help="Release every live session (excluding readonly)."
    ),
    tag: list[str] = typer.Option(  # noqa: B008
        None, "--tag", help="Release sessions carrying this tag (repeatable)."
    ),
    idle_since: str | None = typer.Option(
        None,
        "--idle-since",
        help="Release sessions idle longer than this duration (e.g. '1h', '30m', '2d').",
    ),
    confirm: bool = typer.Option(
        False, "--yes", help="Skip the confirmation prompt for bulk releases."
    ),
) -> None:
    """Release sessions from chubby's management.

    Runs each session's ``teardown`` script if configured, kills the
    wrapper, removes any chubby-owned worktree, and drops the session
    from the in-memory registry. The on-disk JSONL transcript is
    untouched, so ``claude --resume <id>`` still works outside chubby.
    """
    bulk_filters_used = all_ or bool(tag) or idle_since is not None
    if names and bulk_filters_used:
        raise typer.BadParameter(
            "pass either positional names or filters (--all/--tag/--idle-since), not both"
        )
    if not names and not bulk_filters_used:
        raise typer.BadParameter(
            "specify at least one session name, or use --all / --tag / --idle-since"
        )

    idle_threshold_ms: int | None = None
    if idle_since is not None:
        idle_threshold_ms = int(time.time() * 1000) - _parse_duration_ms(idle_since)

    async def go() -> None:
        c = Client(paths.sock_path())
        try:
            sessions = (await c.call("list_sessions", {})).get("sessions", [])
            targets = _select_targets(
                sessions,
                names=names,
                all_=all_,
                tags=tag,
                idle_threshold_ms=idle_threshold_ms,
            )
            if not targets:
                if OUT.mode is Mode.PRETTY:
                    typer.echo("no targets")
                else:
                    OUT.list([])
                return
            if bulk_filters_used and not confirm and OUT.mode is Mode.PRETTY:
                preview = ", ".join(s["name"] for s in targets)
                typer.confirm(f"release {len(targets)} sessions ({preview})?", abort=True)

            released: list[dict[str, Any]] = []
            failed: list[tuple[str, str]] = []
            for s in targets:
                try:
                    await _release_one(c, s["id"])
                    released.append({"id": s["id"], "name": s["name"]})
                except ChubError as e:
                    failed.append((s["name"], str(e)))

            if OUT.mode is Mode.PRETTY:
                if len(targets) == 1 and not failed:
                    typer.echo(f"released {released[0]['name']}")
                else:
                    typer.echo(f"released: {len(released)} ok, {len(failed)} failed")
                    for name, err in failed:
                        typer.echo(f"  x {name}: {err}")
            elif OUT.mode is Mode.QUIET:
                # One id per line for ``xargs -L1`` pipelines. Failures
                # are silently dropped here — quiet is "ids of things
                # that succeeded".
                for r in released:
                    typer.echo(r["id"])
            else:
                # JSON: emit both lists so an outer agent can see what
                # actually happened. Previously emitted ``released``
                # only — and a daemon-side cleanup error would surface
                # as ``[]`` even when the work clearly happened.
                typer.echo(
                    json.dumps(
                        {
                            "released": released,
                            "failed": [{"name": name, "error": err} for name, err in failed],
                        }
                    )
                )
        finally:
            await c.close()

    asyncio.run(go())


def _select_targets(
    sessions: list[dict[str, Any]],
    *,
    names: list[str] | None,
    all_: bool,
    tags: list[str] | None,
    idle_threshold_ms: int | None,
) -> list[dict[str, Any]]:
    # Skip sessions that are already torn down. Readonly sessions are
    # included: chubby doesn't own their process, but ``release`` still
    # drops the rail row, which is what the user wants when cleaning up
    # auto-registered ``claude`` sessions they never spawned.
    live = [s for s in sessions if s["status"] != "dead"]
    if names:
        wanted = set(names)
        matched = [s for s in live if s["name"] in wanted]
        missing = wanted - {s["name"] for s in matched}
        if missing:
            raise typer.BadParameter(f"no live session(s) named: {', '.join(sorted(missing))}")
        return matched
    targets = live
    if tags:
        tagset = set(tags)
        targets = [t for t in targets if tagset.intersection(t.get("tags", []))]
    if idle_threshold_ms is not None:
        targets = [t for t in targets if t.get("last_activity_at", 0) <= idle_threshold_ms]
    return targets


async def _release_one(c: Client, session_id: str) -> None:
    """Try ``release_session`` first; fall back to ``detach_session``
    when the session has no bound claude id yet (release_session
    refuses those because its result tuple needs the id)."""
    try:
        await c.call("release_session", {"id": session_id})
    except ChubError as e:
        if e.code == ErrorCode.INVALID_PAYLOAD and "claude session id" in str(e):
            await c.call("detach_session", {"id": session_id})
        else:
            raise
