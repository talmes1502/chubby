"""Monotonic-ish wall clock with a freeze() context manager for tests."""

from __future__ import annotations

import time
from collections.abc import Generator
from contextlib import contextmanager

_frozen_ms: int | None = None


def now_ms() -> int:
    if _frozen_ms is not None:
        return _frozen_ms
    return int(time.time() * 1000)


@contextmanager
def freeze(ms: int) -> Generator[None, None, None]:
    global _frozen_ms
    prev = _frozen_ms
    _frozen_ms = ms
    try:
        yield
    finally:
        _frozen_ms = prev
