"""Per-session log file writer. ANSI bytes preserved as-is."""

from __future__ import annotations

import asyncio
from pathlib import Path
from typing import IO

from chubby.daemon.clock import now_ms


class LogWriter:
    def __init__(
        self, run_logs_dir: Path, *, color: str, session_name: str
    ) -> None:
        self.path = run_logs_dir / f"{session_name}.log"
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._lock = asyncio.Lock()
        self._fh: IO[bytes] = open(self.path, "ab")
        header = (
            f"# chubby session: {session_name} color={color} "
            f"started_at_ms={now_ms()}\n"
        )
        self._fh.write(header.encode())
        self._fh.flush()

    async def append(self, data: bytes) -> None:
        async with self._lock:
            await asyncio.to_thread(self._write, data)

    def _write(self, data: bytes) -> None:
        self._fh.write(data)
        self._fh.flush()

    async def close(self) -> None:
        async with self._lock:
            await asyncio.to_thread(self._fh.close)
