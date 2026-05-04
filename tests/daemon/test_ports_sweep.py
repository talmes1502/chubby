"""Tests for the port-scan sweep that emits ``session_ports_changed``.

We avoid spawning real listeners here (those are exercised by
``test_ports.py``); instead we monkey-patch the underlying
``listening_ports`` helper so the sweep loop can be driven
deterministically in a tight test.
"""

from __future__ import annotations

import os
from typing import Any

import pytest

from chubby.daemon import main as chubbyd_main
from chubby.daemon import ports as ports_mod
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind, SessionStatus


class _FakeSubs:
    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((method, params))


async def test_set_ports_emits_event_on_change() -> None:
    """Direct test of the registry helper that the sweep calls."""
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="t", kind=SessionKind.WRAPPED, cwd="/tmp")
    subs.broadcasts.clear()  # ignore session_added

    changed = await reg.set_ports(s.id, [{"port": 3000, "pid": 42, "address": "127.0.0.1"}])
    assert changed is True
    events = [p for m, p in subs.broadcasts if m == "session_ports_changed"]
    assert len(events) == 1
    assert events[0]["id"] == s.id
    assert events[0]["ports"][0]["port"] == 3000


async def test_set_ports_idempotent_when_unchanged() -> None:
    """Address flapping (127.0.0.1 ↔ 0.0.0.0) for the same (port,
    pid) doesn't emit — the visible-state didn't change."""
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="t", kind=SessionKind.WRAPPED, cwd="/tmp")

    await reg.set_ports(s.id, [{"port": 3000, "pid": 42, "address": "0.0.0.0"}])
    subs.broadcasts.clear()
    changed = await reg.set_ports(s.id, [{"port": 3000, "pid": 42, "address": "127.0.0.1"}])
    assert changed is False
    events = [m for m, _ in subs.broadcasts if m == "session_ports_changed"]
    assert events == []


async def test_sweep_walks_process_tree_and_emits(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """End-to-end sweep tick: register a session, stub the OS-specific
    helpers, drive one iteration, assert the event fires with the
    canned port info."""
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    s = await reg.register(
        name="dev",
        kind=SessionKind.WRAPPED,
        cwd="/tmp",
        pid=os.getpid(),  # any non-None pid; we stub the lookups
    )
    subs.broadcasts.clear()

    async def fake_tree(_root: int) -> list[int]:
        return [_root, 9999]

    async def fake_listening(_pids: list[int]) -> list[ports_mod.PortInfo]:
        return [ports_mod.PortInfo(port=5173, pid=9999, address="127.0.0.1")]

    monkeypatch.setattr(ports_mod, "process_tree", fake_tree)
    monkeypatch.setattr(ports_mod, "listening_ports", fake_listening)

    changed = await chubbyd_main._sweep_ports_once(reg)
    assert changed == 1
    events = [p for m, p in subs.broadcasts if m == "session_ports_changed"]
    assert len(events) == 1
    assert events[0]["id"] == s.id
    assert events[0]["ports"][0]["port"] == 5173
    assert events[0]["ports"][0]["pid"] == 9999


async def test_sweep_skips_dead_sessions(monkeypatch: pytest.MonkeyPatch) -> None:
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="dead", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await reg.update_status(s.id, SessionStatus.DEAD)
    subs.broadcasts.clear()

    called = False

    async def fake_listening(_pids: list[int]) -> list[ports_mod.PortInfo]:
        nonlocal called
        called = True
        return []

    monkeypatch.setattr(ports_mod, "listening_ports", fake_listening)
    changed = await chubbyd_main._sweep_ports_once(reg)
    assert changed == 0
    assert called is False, "shouldn't even probe a DEAD session"


async def test_sweep_skips_sessions_without_pid(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Readonly sessions don't have a wrapper pid — skip them."""
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    await reg.register(name="ro", kind=SessionKind.READONLY, cwd="/tmp", pid=None)
    subs.broadcasts.clear()

    called = False

    async def fake_listening(_pids: list[int]) -> list[ports_mod.PortInfo]:
        nonlocal called
        called = True
        return []

    monkeypatch.setattr(ports_mod, "listening_ports", fake_listening)
    changed = await chubbyd_main._sweep_ports_once(reg)
    assert changed == 0
    assert called is False
