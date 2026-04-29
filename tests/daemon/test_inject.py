"""Tests for Registry.inject and the wrapper-writer attach path."""

from __future__ import annotations

import pytest

from chub.daemon.registry import Registry
from chub.daemon.session import SessionKind
from chub.proto.errors import ChubError, ErrorCode


async def test_inject_writes_to_attached_writer() -> None:
    captured: list[bytes] = []

    async def write(b: bytes) -> None:
        captured.append(b)

    r = Registry(hub_run_id="hr_t")
    s = await r.register(name="x", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await r.attach_wrapper(s.id, write)
    await r.inject(s.id, b"hello\n")
    assert captured
    assert b"inject_to_pty" in captured[0]


async def test_inject_without_attached_writer_raises() -> None:
    r = Registry(hub_run_id="hr_t")
    s = await r.register(name="ro", kind=SessionKind.WRAPPED, cwd="/tmp")
    with pytest.raises(ChubError) as exc:
        await r.inject(s.id, b"x")
    assert exc.value.code is ErrorCode.WRAPPER_UNREACHABLE


async def test_inject_tmux_kind_dispatches_to_tmux_module(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """For TMUX_ATTACHED sessions, Registry.inject delegates to inject_tmux."""
    captured: list[tuple[str, bytes]] = []

    async def fake_inject_tmux(session: object, payload: bytes) -> None:
        captured.append((getattr(session, "name", "?"), payload))

    import chub.daemon.attach.tmux as tmux_mod

    monkeypatch.setattr(tmux_mod, "inject_tmux", fake_inject_tmux)
    r = Registry(hub_run_id="hr_t")
    s = await r.register(
        name="tm", kind=SessionKind.TMUX_ATTACHED, cwd="/tmp", tmux_target="x:0.0"
    )
    await r.inject(s.id, b"x")
    assert captured == [("tm", b"x")]


async def test_inject_tmux_without_target_raises() -> None:
    """A TMUX_ATTACHED session created without tmux_target should raise."""
    r = Registry(hub_run_id="hr_t")
    s = await r.register(name="tm", kind=SessionKind.TMUX_ATTACHED, cwd="/tmp")
    with pytest.raises(ChubError) as exc:
        await r.inject(s.id, b"x")
    assert exc.value.code is ErrorCode.TMUX_TARGET_INVALID
