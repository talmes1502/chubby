"""``chubby start`` — one-command bootstrap (daemon + hooks + tui).

Behaviour:
  1. Ensure chubbyd is running (start it detached if not).
  2. Wait up to 5s for the socket + ping to succeed.
  3. Install Claude hooks (idempotent — already-installed hooks aren't duplicated).
  4. Optionally bulk-register every promote_required candidate as a readonly
     session (``--auto-attach``).
  5. Unless ``--no-tui`` is set, ``os.execv`` into the chubby-tui binary.
"""

from __future__ import annotations

import asyncio
import os
import subprocess
import sys
import time

import typer

from chubby.cli.client import Client
from chubby.daemon import paths


def _pid_alive(pid: int) -> bool:
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def _read_pid() -> int | None:
    p = paths.pid_path()
    if not p.exists():
        return None
    try:
        return int(p.read_text().strip() or "0") or None
    except (ValueError, OSError):
        return None


async def _can_connect(timeout: float = 0.5) -> bool:
    """Return True if a ping succeeds against the daemon socket."""
    sock = paths.sock_path()
    if not sock.exists():
        return False
    try:
        c = Client(sock)
        try:
            await asyncio.wait_for(c.call("ping", {"message": "start"}), timeout=timeout)
            return True
        finally:
            await c.close()
    except (TimeoutError, OSError, ConnectionError):
        return False
    except Exception:
        return False


async def _wait_for_daemon(deadline: float) -> bool:
    while time.monotonic() < deadline:
        if await _can_connect():
            return True
        await asyncio.sleep(0.1)
    return False


def _spawn_daemon() -> int:
    proc = subprocess.Popen(
        [sys.executable, "-m", "chubby.daemon.main"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
        env=os.environ.copy(),
    )
    return proc.pid


async def _ensure_daemon() -> tuple[int, bool]:
    """Return (pid, started_now). Starts a daemon if none is reachable."""
    existing = _read_pid()
    if existing is not None and _pid_alive(existing) and await _can_connect():
        return existing, False
    spawn_pid = _spawn_daemon()
    deadline = time.monotonic() + 5.0
    if not await _wait_for_daemon(deadline):
        raise typer.Exit(code=1)
    # Re-read the pid file: the spawned process may have been a re-exec
    # parent; the on-disk pid is authoritative.
    pid = _read_pid() or spawn_pid
    return pid, True


async def _auto_attach_readonly() -> int:
    """Bulk-register every promote_required candidate. Returns count."""
    c = Client(paths.sock_path())
    try:
        scan = await c.call("scan_candidates", {})
        cands = [
            x
            for x in scan["candidates"]
            if not x.get("already_attached") and x.get("classification") == "promote_required"
        ]
        n = 0
        for x in cands:
            nm = f"{os.path.basename(x['cwd'].rstrip('/'))}-{x['pid']}"
            await c.call(
                "attach_existing_readonly",
                {"pid": x["pid"], "cwd": x["cwd"], "name": nm},
            )
            n += 1
        return n
    finally:
        await c.close()


def run(
    auto_attach: bool = typer.Option(
        False,
        "--auto-attach/--no-auto-attach",
        help="Bulk-register every promote_required candidate as readonly.",
    ),
    no_hooks: bool = typer.Option(False, "--no-hooks", help="Skip Claude hook install."),
    no_tui: bool = typer.Option(
        False, "--no-tui", help="Don't launch the TUI; just bootstrap and exit."
    ),
) -> None:
    async def go() -> tuple[int, bool, int]:
        pid, started = await _ensure_daemon()
        attached = 0
        if auto_attach:
            attached = await _auto_attach_readonly()
        return pid, started, attached

    pid, started, attached = asyncio.run(go())
    verb = "started" if started else "running"
    typer.echo(f"daemon {verb} (pid {pid})")

    if not no_hooks:
        from chubby.cli.commands import install_hooks

        # Pass explicit values for every option — calling a typer-decorated
        # function directly leaves un-passed kwargs as OptionInfo sentinels,
        # which are truthy. ``auto_register=True`` re-installs the
        # SessionStart hook every ``chubby start``, which silently undid
        # users who explicitly opted out.
        install_hooks.run(dry_run=False, auto_register=False)
        typer.echo("hooks installed")

    if auto_attach:
        typer.echo(f"attached {attached} readonly session(s)")

    if no_tui:
        return

    typer.echo("opening tui...")
    from chubby.cli.commands import tui as tui_mod

    # Pass explicit values for every option — calling a typer-decorated
    # function directly leaves un-passed kwargs as OptionInfo sentinels,
    # which then leak into env and break os.execvpe().
    tui_mod.run(force_download=False, focus=None, detached=False)
