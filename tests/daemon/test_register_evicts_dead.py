"""Tests for the register-time eviction of dead sessions sharing a
name. The user-visible behavior is "Ctrl+P respawns a dead session in
place" — a single rail row with the same name flips from dead to
live. The daemon enforces this by dropping any dead row matching the
incoming name when a new wrapper registers.

Without this, the rename-dance the TUI used to do (spawn temp-r,
rename to temp) would leave two rows-same-name in memory: the dead
original and the new live one.
"""

from __future__ import annotations

from typing import Any

import pytest

from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind, SessionStatus
from chubby.proto.errors import ChubError


class _FakeSubs:
    """Captures every broadcast for assertion. Mirrors the shape used
    by the daemon's SubscriptionHub so Registry's _emit can call it."""

    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((method, params))


def _make_reg() -> tuple[Registry, _FakeSubs]:
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_test", subs=subs)  # type: ignore[arg-type]
    return reg, subs


async def test_register_with_unique_name_emits_only_session_added() -> None:
    reg, subs = _make_reg()
    s = await reg.register(name="alpha", kind=SessionKind.WRAPPED, cwd="/tmp")
    assert s.name == "alpha"
    assert s.status is SessionStatus.IDLE
    methods = [m for m, _ in subs.broadcasts]
    assert methods == ["session_added"]


async def test_register_evicts_dead_with_same_name() -> None:
    """The exact respawn scenario: register a session, mark it dead,
    register again with the same name. The dead row must be evicted
    from in-memory state and a session_removed event must fire BEFORE
    the new session_added so subscribers never see two rows-same-name.
    """
    reg, subs = _make_reg()
    old = await reg.register(name="temp", kind=SessionKind.WRAPPED, cwd="/tmp")
    await reg.update_status(old.id, SessionStatus.DEAD)
    subs.broadcasts.clear()
    new = await reg.register(name="temp", kind=SessionKind.WRAPPED, cwd="/tmp")
    assert new.id != old.id
    all_sessions = await reg.list_all()
    assert [s.id for s in all_sessions] == [new.id]
    # Order: removed (old dead) then added (new live).
    methods = [m for m, _ in subs.broadcasts]
    assert methods == ["session_removed", "session_added"]
    removed_id = next(p["id"] for m, p in subs.broadcasts if m == "session_removed")
    assert removed_id == old.id


async def test_register_does_not_evict_live_with_same_name() -> None:
    """Registering with a name that's currently held by a LIVE session
    must raise NAME_TAKEN, not silently evict the live one. Eviction
    is only correct when the prior session is dead."""
    reg, _ = _make_reg()
    await reg.register(name="alive", kind=SessionKind.WRAPPED, cwd="/tmp")
    with pytest.raises(ChubError):
        await reg.register(name="alive", kind=SessionKind.WRAPPED, cwd="/tmp")


async def test_register_evicts_multiple_dead_with_same_name() -> None:
    """Edge case: more than one dead row shares the name (shouldn't
    happen in steady state, but defensive). All dead rows must be
    evicted, one session_removed event per."""
    from chubby.daemon.clock import now_ms
    from chubby.daemon.session import Session

    reg, subs = _make_reg()
    a = await reg.register(name="dup", kind=SessionKind.WRAPPED, cwd="/tmp")
    await reg.update_status(a.id, SessionStatus.DEAD)
    # Manually inject a second dead row to simulate a pre-eviction-era
    # state where two-rows-same-name slipped in.
    b = Session(
        id="s_dup_2",
        hub_run_id="hr_test",
        name="dup",
        color="#000000",
        kind=SessionKind.WRAPPED,
        cwd="/tmp",
        created_at=now_ms(),
        last_activity_at=now_ms(),
        status=SessionStatus.DEAD,
    )
    reg._by_id[b.id] = b
    subs.broadcasts.clear()
    new = await reg.register(name="dup", kind=SessionKind.WRAPPED, cwd="/tmp")
    all_sessions = await reg.list_all()
    assert [s.id for s in all_sessions] == [new.id]
    removed_ids = sorted(p["id"] for m, p in subs.broadcasts if m == "session_removed")
    assert removed_ids == sorted([a.id, b.id])
