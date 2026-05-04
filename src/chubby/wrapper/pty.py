"""PTY wrapper: spawns a child under a pty, exposes async iterator over output,
and ``write_user()`` to inject bytes into the child's stdin."""

from __future__ import annotations

import asyncio
import errno
import os
import signal
from collections.abc import AsyncIterator

import ptyprocess


# Env vars that signal "we're already inside a Claude Code agent
# context" — passed through to a child claude they'd confuse the
# child's session id resolution and hook firing. Strip them before
# spawning the wrapped process. Same defensive list moltty applies
# in its main process. ``CLAUDECODE`` is the legacy spelling.
_AGENT_ENV_VARS_TO_STRIP: tuple[str, ...] = (
    "CLAUDE_CODE",
    "CLAUDECODE",
    "CLAUDE_CODE_ENTRYPOINT",
    "CLAUDE_SESSION_ID",
    # Same for our own marker — a chubby spawned from inside another
    # chubby's wrapped claude must look like a fresh terminal so the
    # inner CLI doesn't auto-flip output to JSON or confuse hooks.
    "CHUBBY_AGENT",
)


def _build_child_env(extra: dict[str, str] | None) -> dict[str, str]:
    """Inherit the parent env minus the agent-context markers, then
    merge any caller overrides. Inject ``TERM_PROGRAM=chubby`` so
    claude/other tools can detect they're running under chubby, and
    ``FORCE_HYPERLINK=1`` so OSC 8 hyperlinks render in our bounded
    vt grid (xterm-style emulators sometimes gate hyperlinks on a
    terminfo capability we don't fully advertise)."""
    env = {k: v for k, v in os.environ.items() if k not in _AGENT_ENV_VARS_TO_STRIP}
    env["TERM_PROGRAM"] = "chubby"
    env.setdefault("FORCE_HYPERLINK", "1")
    if extra:
        env.update(extra)
    return env


class PtySession:
    def __init__(
        self,
        argv: list[str],
        *,
        cwd: str,
        env: dict[str, str] | None = None,
    ) -> None:
        self.argv = argv
        self.cwd = cwd
        self.env: dict[str, str] = _build_child_env(env)
        self._proc: ptyprocess.PtyProcess | None = None
        self._closed = asyncio.Event()

    async def start(self) -> None:
        self._proc = await asyncio.to_thread(
            ptyprocess.PtyProcess.spawn, self.argv, cwd=self.cwd, env=self.env
        )

    @property
    def pid(self) -> int:
        assert self._proc is not None
        return int(self._proc.pid)

    async def iter_output(self) -> AsyncIterator[bytes]:
        assert self._proc is not None
        loop = asyncio.get_running_loop()
        while True:
            try:
                chunk = await loop.run_in_executor(None, self._read_chunk)
            except EOFError:
                self._closed.set()
                return
            if not chunk:
                self._closed.set()
                return
            yield chunk

    def _read_chunk(self) -> bytes:
        assert self._proc is not None
        try:
            return bytes(self._proc.read(4096))
        except EOFError:
            return b""

    async def write_user(self, payload: bytes) -> None:
        assert self._proc is not None
        await asyncio.to_thread(self._proc.write, payload)

    async def resize(self, rows: int, cols: int) -> None:
        assert self._proc is not None
        await asyncio.to_thread(self._proc.setwinsize, rows, cols)

    async def get_size(self) -> tuple[int, int]:
        """Return the PTY's current (rows, cols). Used by the
        redraw-claude path to do a transient resize-toggle that
        forces claude to do a full redraw (claude only re-lays-out
        on actual size change, not on bare SIGWINCH)."""
        assert self._proc is not None
        rows, cols = await asyncio.to_thread(self._proc.getwinsize)
        return int(rows), int(cols)

    async def terminate(self) -> None:
        if self._proc is None:
            return
        try:
            self._proc.kill(signal.SIGTERM)
        except OSError as e:
            if e.errno != errno.ESRCH:
                raise
        self._closed.set()

    def signal_child(self, sig: int = signal.SIGTERM) -> None:
        """Send ``sig`` to the child process without setting ``closed``.

        Used by the wrapper's restart loop to kill claude in-place so the
        PTY pump's iter_output() drains EOF and returns. Unlike
        ``terminate()``, this does NOT set the closed flag — the iterator
        sets it once the read returns empty, and the wrapper's main loop
        relies on that ordering to know when it's safe to launch a fresh
        claude.
        """
        if self._proc is None:
            return
        try:
            self._proc.kill(sig)
        except OSError as e:
            if e.errno != errno.ESRCH:
                raise

    @property
    def closed(self) -> asyncio.Event:
        return self._closed
