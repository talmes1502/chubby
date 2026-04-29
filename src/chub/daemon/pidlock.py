"""Filesystem PID lock with stale-cleanup. Used to enforce one chubd per user."""

from __future__ import annotations

import errno
import os
from collections.abc import Generator
from contextlib import contextmanager
from pathlib import Path


class PidLockBusy(Exception):
    def __init__(self, pid: int, path: Path) -> None:
        super().__init__(f"another chubd is running (pid {pid}, lock {path})")
        self.pid = pid
        self.path = path


def _alive(pid: int) -> bool:
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


@contextmanager
def acquire(path: Path) -> Generator[None, None, None]:
    path.parent.mkdir(parents=True, exist_ok=True)
    while True:
        try:
            fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        except OSError as e:
            if e.errno != errno.EEXIST:
                raise
            try:
                existing = int(path.read_text().strip() or "0")
            except (ValueError, FileNotFoundError):
                existing = 0
            if _alive(existing):
                raise PidLockBusy(existing, path) from None
            try:
                path.unlink()
            except FileNotFoundError:
                pass
            continue
        try:
            os.write(fd, f"{os.getpid()}\n".encode())
        finally:
            os.close(fd)
        try:
            yield
        finally:
            try:
                path.unlink()
            except FileNotFoundError:
                pass
        return
