"""Run a project's setup/teardown/run scripts with login-shell parity.

Each command is exec'd via ``zsh -lc <cmd>`` (Linux: ``bash -lc``) so
it picks up the user's interactive rc files (``.zshrc`` / ``.bashrc``)
— mirroring what Superset achieves by allocating a real PTY for the
script. We use plain ``create_subprocess_exec`` instead of a PTY
because we don't need interactive features here, just rcfile sourcing
+ environment parity.

Per-command timeout (default 60 s) with two-stage shutdown: SIGHUP
first (zsh under ``-l`` traps SIGTERM and stays alive — same lesson
Superset's daemon-supervision doc surfaced), then SIGKILL after a
grace window.

On failure, we keep only the last 4 KB of combined stdout/stderr —
enough for the user to see *why* it failed without flooding logs.
"""

from __future__ import annotations

import asyncio
import logging
import os
import signal
import sys
from dataclasses import dataclass
from pathlib import Path

log = logging.getLogger(__name__)


_DEFAULT_TIMEOUT_S = 60.0
_DEFAULT_TAIL_BYTES = 4096
_KILL_GRACE_S = 2.0


@dataclass
class LifecycleResult:
    """Result of running a *single* lifecycle script. The ``Skipped``
    case fires when the command list is empty — callers can treat it
    interchangeably with ``Ok`` in their happy path."""
    status: str  # "ok" | "skipped" | "failed"
    output_tail: str = ""
    exit_code: int | None = None
    signal: int | None = None
    timed_out: bool = False
    failed_command: str | None = None


def _login_shell_argv(cmd: str) -> list[str]:
    """Pick the user's login shell with ``-lc <cmd>``. On macOS that's
    almost always zsh; on Linux it varies. The ``$SHELL`` env var is
    a reasonable proxy for "what the user actually uses"."""
    shell = os.environ.get("SHELL") or ("/bin/zsh" if sys.platform == "darwin" else "/bin/bash")
    return [shell, "-lc", cmd]


async def _run_one(
    cmd: str,
    cwd: Path,
    env: dict[str, str],
    timeout_s: float,
    tail_bytes: int,
) -> LifecycleResult:
    """Run a single command. Returns ``ok`` or ``failed``."""
    try:
        proc = await asyncio.create_subprocess_exec(
            *_login_shell_argv(cmd),
            cwd=str(cwd),
            env=env,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT,
        )
    except FileNotFoundError as e:
        return LifecycleResult(
            status="failed",
            output_tail=f"failed to launch shell: {e}",
            failed_command=cmd,
        )
    timed_out = False
    try:
        stdout, _ = await asyncio.wait_for(
            proc.communicate(), timeout=timeout_s
        )
    except asyncio.TimeoutError:
        timed_out = True
        # SIGHUP first — zsh under ``-l`` ignores SIGTERM but honours
        # SIGHUP (kernel sends it on real-TTY hangup, so shells expect
        # it). Wait briefly, then SIGKILL if still alive.
        try:
            proc.send_signal(signal.SIGHUP)
        except ProcessLookupError:
            pass
        try:
            stdout, _ = await asyncio.wait_for(
                proc.communicate(), timeout=_KILL_GRACE_S
            )
        except asyncio.TimeoutError:
            try:
                proc.kill()
            except ProcessLookupError:
                pass
            try:
                stdout, _ = await asyncio.wait_for(
                    proc.communicate(), timeout=1.0
                )
            except asyncio.TimeoutError:
                stdout = b""
    text = stdout.decode("utf-8", errors="replace") if stdout else ""
    if len(text) > tail_bytes:
        text = text[-tail_bytes:]
    rc = proc.returncode
    if timed_out or (rc is not None and rc != 0):
        return LifecycleResult(
            status="failed",
            output_tail=text,
            exit_code=rc,
            signal=None,
            timed_out=timed_out,
            failed_command=cmd,
        )
    return LifecycleResult(status="ok", output_tail=text, exit_code=0)


async def run_lifecycle(
    commands: list[str],
    cwd: Path,
    env: dict[str, str] | None = None,
    timeout_s: float = _DEFAULT_TIMEOUT_S,
    tail_bytes: int = _DEFAULT_TAIL_BYTES,
) -> LifecycleResult:
    """Run a list of lifecycle commands sequentially in ``cwd``.

    Each command runs with ``timeout_s`` and a fresh shell. If any
    command fails, the result captures that command's exit code +
    output tail and we stop — later commands are skipped. The session
    can still be salvaged via ``--force-delete`` (Superset's "force
    skip" pattern, surfaced via teardown's ``status="failed"`` not
    being blocking).

    Empty ``commands`` returns ``status="skipped"`` so callers can
    branch on that without checking lengths.
    """
    if not commands:
        return LifecycleResult(status="skipped")
    proc_env = {**os.environ, **(env or {})}
    last_ok = LifecycleResult(status="ok")
    for cmd in commands:
        res = await _run_one(cmd, cwd, proc_env, timeout_s, tail_bytes)
        if res.status == "failed":
            return res
        last_ok = res
    return last_ok
