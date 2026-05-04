"""Long-running ``run`` commands tied to session lifecycle.

The ``.chubby/config.json`` schema accepts a ``run`` array (e.g.
``["bun dev"]``) for on-demand background commands. Setup/teardown
use ``run_lifecycle`` which awaits completion — that's the wrong
shape for ``bun dev``, which is supposed to keep running for the
session's whole life.

This module owns those long-running processes:

* ``start`` launches one via ``zsh -lc <cmd>`` (login-shell parity,
  same as setup/teardown), redirects stdout+stderr to a per-process
  log file under ``runs/<hub_run_id>/logs/``, and tracks the PID +
  log path on a per-(session_id, index) key.
* ``stop`` and ``stop_all_for_session`` terminate the process —
  SIGHUP first (zsh under ``-l`` ignores SIGTERM), SIGKILL after a
  grace window. Used by ``release_session`` / ``detach_session``
  to clean up automatically.
* A watcher coroutine awaits each process: when it exits on its own
  (e.g. a typo in ``bun dev`` makes it fail fast), the registry
  entry is removed so the next ``:run 0`` re-launches it.
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

_KILL_GRACE_S = 2.0


@dataclass
class RunProcess:
    """Metadata for one running ``run`` command, exposed via list_runs
    and used by :run / :stop-run handlers. The ``proc`` ref is kept
    private to the registry."""

    session_id: str
    index: int
    cmd: str
    pid: int
    log_path: Path
    started_at_ms: int


def _login_shell_argv(cmd: str) -> list[str]:
    shell = os.environ.get("SHELL") or ("/bin/zsh" if sys.platform == "darwin" else "/bin/bash")
    return [shell, "-lc", cmd]


class RunProcessRegistry:
    """In-memory registry of long-running ``run`` processes, keyed by
    ``(session_id, index)``. Not persisted: a daemon restart leaves
    the dev servers running (they have their own pids), but chubby
    forgets about them. That's the right tradeoff — restart is rare
    and the user can ``ps`` for a stuck process."""

    def __init__(self) -> None:
        self._procs: dict[tuple[str, int], asyncio.subprocess.Process] = {}
        self._meta: dict[tuple[str, int], RunProcess] = {}
        self._watchers: set[asyncio.Task[None]] = set()
        self._lock = asyncio.Lock()

    async def start(
        self,
        *,
        session_id: str,
        index: int,
        cmd: str,
        cwd: Path,
        env: dict[str, str],
        log_path: Path,
        clock_ms: int,
    ) -> RunProcess:
        """Launch ``cmd`` in the background. Raises ``RuntimeError`` if
        the same ``(session_id, index)`` is already running so callers
        can surface "already running, pid X" without spawning a duplicate.
        """
        key = (session_id, index)
        async with self._lock:
            if key in self._procs:
                existing = self._meta[key]
                raise RuntimeError(
                    f"run {index} for session {session_id} is already running as pid {existing.pid}"
                )
            log_path.parent.mkdir(parents=True, exist_ok=True)
            log_fh = log_path.open("ab", buffering=0)
            try:
                proc = await asyncio.create_subprocess_exec(
                    *_login_shell_argv(cmd),
                    cwd=str(cwd),
                    env={**os.environ, **env},
                    stdin=asyncio.subprocess.DEVNULL,
                    stdout=log_fh,
                    stderr=asyncio.subprocess.STDOUT,
                    # Detach from chubbyd's process group so a daemon
                    # SIGINT (Ctrl+C in chubbyd's foreground term)
                    # doesn't accidentally kill the user's dev server.
                    start_new_session=True,
                )
            finally:
                # Subprocess inherits the fd; we close our handle so the
                # log file isn't held open by the daemon.
                log_fh.close()
            assert proc.pid is not None
            meta = RunProcess(
                session_id=session_id,
                index=index,
                cmd=cmd,
                pid=proc.pid,
                log_path=log_path,
                started_at_ms=clock_ms,
            )
            self._procs[key] = proc
            self._meta[key] = meta

        # Start a watcher OUTSIDE the lock — it will re-acquire to
        # remove the entry when the process exits. Holding the lock
        # while creating the task is harmless but unnecessary.
        watcher = asyncio.create_task(self._watch(key, proc))
        self._watchers.add(watcher)
        watcher.add_done_callback(self._watchers.discard)
        return meta

    async def _watch(self, key: tuple[str, int], proc: asyncio.subprocess.Process) -> None:
        """Await the process and drop the registry entry when it exits.
        Without this, a ``bun dev`` that died on its own would still show
        up in ``list`` and refuse a re-launch."""
        try:
            await proc.wait()
        except asyncio.CancelledError:
            return
        async with self._lock:
            # Only drop if it's still the same proc — start() under the
            # lock would have replaced it if a re-launch raced us.
            if self._procs.get(key) is proc:
                self._procs.pop(key, None)
                self._meta.pop(key, None)

    async def stop(self, session_id: str, index: int) -> bool:
        """Stop run ``index`` for ``session_id``. Returns ``True`` if a
        process was actually killed, ``False`` if there was nothing to
        stop."""
        key = (session_id, index)
        async with self._lock:
            proc = self._procs.get(key)
            if proc is None:
                return False
        await self._terminate(proc)
        # The watcher will clear the registry entry — but await that
        # explicitly so the caller can rely on list_for_session
        # reflecting reality immediately after stop returns.
        async with self._lock:
            self._procs.pop(key, None)
            self._meta.pop(key, None)
        return True

    async def stop_all_for_session(self, session_id: str) -> int:
        """Stop every run process belonging to ``session_id``. Returns
        the count actually stopped. Called from ``release_session`` and
        ``detach_session`` so the user's dev servers come down with the
        session."""
        async with self._lock:
            keys = [k for k in self._procs if k[0] == session_id]
            procs = [self._procs[k] for k in keys]
        # Terminate concurrently — many sessions, many dev servers.
        await asyncio.gather(*(self._terminate(p) for p in procs), return_exceptions=True)
        async with self._lock:
            for k in keys:
                self._procs.pop(k, None)
                self._meta.pop(k, None)
        return len(keys)

    async def _terminate(self, proc: asyncio.subprocess.Process) -> None:
        """SIGHUP-then-SIGKILL with a grace window — same protocol the
        lifecycle scripts use, since the child is a login shell."""
        if proc.returncode is not None:
            return
        try:
            proc.send_signal(signal.SIGHUP)
        except ProcessLookupError:
            return
        try:
            await asyncio.wait_for(proc.wait(), timeout=_KILL_GRACE_S)
            return
        except TimeoutError:
            pass
        try:
            proc.kill()
        except ProcessLookupError:
            return
        try:
            await asyncio.wait_for(proc.wait(), timeout=1.0)
        except TimeoutError:
            log.warning("run process pid=%s ignored SIGKILL", proc.pid)

    def list_for_session(self, session_id: str) -> list[RunProcess]:
        """Snapshot of currently-running entries for one session."""
        return [v for (sid, _idx), v in self._meta.items() if sid == session_id]

    def is_running(self, session_id: str, index: int) -> bool:
        return (session_id, index) in self._procs
