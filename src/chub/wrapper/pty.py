"""PTY wrapper: spawns a child under a pty, exposes async iterator over output,
and ``write_user()`` to inject bytes into the child's stdin."""

from __future__ import annotations

import asyncio
import errno
import os
import signal
from collections.abc import AsyncIterator

import ptyprocess


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
        self.env: dict[str, str] = {**os.environ, **(env or {})}
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

    async def terminate(self) -> None:
        if self._proc is None:
            return
        try:
            self._proc.kill(signal.SIGTERM)
        except OSError as e:
            if e.errno != errno.ESRCH:
                raise
        self._closed.set()

    @property
    def closed(self) -> asyncio.Event:
        return self._closed
