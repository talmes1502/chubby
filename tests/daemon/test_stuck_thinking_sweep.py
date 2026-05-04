"""The daemon's _sweep_once helper auto-reverts sessions stuck in
THINKING when their last_activity_at is older than the configured
max-age. Drives the sync helper directly so the test doesn't depend on
real time passing.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

from chubby.daemon.clock import now_ms
from chubby.daemon.main import _sweep_once
from chubby.daemon.persistence import Database
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind, SessionStatus


class _FakeSubs:
    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, event_method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((event_method, params))


async def test_sweep_reverts_stuck_thinking(tmp_path: Path) -> None:
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = await reg.register(
            name="stuck",
            kind=SessionKind.WRAPPED,
            cwd=str(tmp_path),
        )
        # Force last_activity_at into the past (15 minutes ago) and flip
        # to THINKING — simulates an inject that never got a response.
        async with reg._lock:
            obj = reg._by_id[s.id]
            obj.status = SessionStatus.THINKING
            obj.last_activity_at = now_ms() - 15 * 60 * 1000

        n = await _sweep_once(reg, max_age_s=600.0)  # 10-min threshold
        assert n == 1
        assert (await reg.get(s.id)).status is SessionStatus.IDLE
    finally:
        await db.close()


async def test_sweep_leaves_recent_thinking_alone(tmp_path: Path) -> None:
    """A session that's been THINKING for less than max_age_s is genuine
    in-flight work — sweep must not touch it."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = await reg.register(
            name="alive",
            kind=SessionKind.WRAPPED,
            cwd=str(tmp_path),
        )
        async with reg._lock:
            obj = reg._by_id[s.id]
            obj.status = SessionStatus.THINKING
            # 30 seconds ago — well under any reasonable threshold.
            obj.last_activity_at = now_ms() - 30 * 1000

        n = await _sweep_once(reg, max_age_s=600.0)
        assert n == 0
        assert (await reg.get(s.id)).status is SessionStatus.THINKING
    finally:
        await db.close()


async def test_sweep_ignores_idle_and_dead(tmp_path: Path) -> None:
    """Only THINKING sessions are candidates — IDLE/AWAITING_USER/DEAD
    sessions stay untouched even if their last_activity_at is ancient."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s_idle = await reg.register(
            name="i",
            kind=SessionKind.WRAPPED,
            cwd=str(tmp_path),
        )
        s_dead = await reg.register(
            name="d",
            kind=SessionKind.WRAPPED,
            cwd=str(tmp_path),
        )
        async with reg._lock:
            for sid, status in (
                (s_idle.id, SessionStatus.IDLE),
                (s_dead.id, SessionStatus.DEAD),
            ):
                reg._by_id[sid].status = status
                reg._by_id[sid].last_activity_at = now_ms() - 60 * 60 * 1000

        n = await _sweep_once(reg, max_age_s=600.0)
        assert n == 0
        assert (await reg.get(s_idle.id)).status is SessionStatus.IDLE
        assert (await reg.get(s_dead.id)).status is SessionStatus.DEAD
    finally:
        await db.close()
