"""Tests for ``chubby.daemon.notify`` and the AWAITING_USER hook."""

from __future__ import annotations

import asyncio
import sys
from typing import Any

import pytest

from chubby.daemon import notify as notify_mod
from chubby.daemon.notify import _esc, notify
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind, SessionStatus


class _FakeProc:
    def __init__(self) -> None:
        self.returncode: int | None = 0

    async def wait(self) -> int:
        return 0


async def test_notify_invokes_subprocess(monkeypatch: pytest.MonkeyPatch) -> None:
    captured: list[tuple[Any, ...]] = []

    async def fake_create(*args: Any, **kwargs: Any) -> _FakeProc:
        captured.append(args)
        return _FakeProc()

    monkeypatch.setattr(asyncio, "create_subprocess_exec", fake_create)
    await notify("title", "body")
    assert captured, "expected create_subprocess_exec to be called"
    if sys.platform == "darwin":
        assert captured[0][0] == "osascript"
        joined = " ".join(str(a) for a in captured[0])
        assert "title" in joined and "body" in joined
    elif sys.platform.startswith("linux"):
        assert captured[0] == ("notify-send", "title", "body")


async def test_notify_swallows_missing_binary(
    monkeypatch: pytest.MonkeyPatch,
    caplog: pytest.LogCaptureFixture,
) -> None:
    if not (sys.platform == "darwin" or sys.platform.startswith("linux")):
        pytest.skip("notify is a no-op on this platform")

    async def boom(*args: Any, **kwargs: Any) -> _FakeProc:
        raise FileNotFoundError(args[0])

    monkeypatch.setattr(asyncio, "create_subprocess_exec", boom)
    with caplog.at_level("WARNING", logger=notify_mod.__name__):
        await notify("t", "b")
    assert any("OS notifier not available" in m for m in caplog.messages)


def test_esc_escapes_quotes_and_backslashes() -> None:
    # Backslashes must be escaped before quotes; otherwise the inserted \" gets
    # double-escaped and the AppleScript breaks.
    assert _esc('say "hi"') == 'say \\"hi\\"'
    assert _esc("a\\b") == "a\\\\b"


async def test_update_status_awaiting_user_fires_notify(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    notified: list[tuple[str, str]] = []

    async def fake_notify(title: str, body: str) -> None:
        notified.append((title, body))

    # Patch the symbol at the import site (registry imports lazily inside the method).
    monkeypatch.setattr(notify_mod, "notify", fake_notify)

    r = Registry(hub_run_id="hr_t")
    s = await r.register(name="alpha", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await r.update_status(s.id, SessionStatus.AWAITING_USER)

    # Drain the notify task.
    pending = list(r._notify_tasks)
    if pending:
        await asyncio.gather(*pending)

    assert notified == [("chubby: alpha", "session is awaiting your input")]


async def test_update_status_non_awaiting_does_not_notify(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    called = False

    async def fake_notify(title: str, body: str) -> None:
        nonlocal called
        called = True

    monkeypatch.setattr(notify_mod, "notify", fake_notify)

    r = Registry(hub_run_id="hr_t")
    s = await r.register(name="alpha", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await r.update_status(s.id, SessionStatus.THINKING)

    # No tasks scheduled; nothing should fire even after a yield.
    await asyncio.sleep(0)
    assert called is False
