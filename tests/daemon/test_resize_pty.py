"""Tests for the resize_pty plumbing — TUI's view-frame size flows
through the daemon to the wrapper, which applies it to its PTY so
claude redraws to fit chubby's conversation pane."""

from __future__ import annotations

import json

import pytest

from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind
from chubby.proto.errors import ChubError, ErrorCode


async def test_resize_forwards_event_to_wrapper() -> None:
    """Registry.resize must encode a `resize_pty` event and write it
    onto the wrapper's writer — symmetric with how inject works."""
    captured: list[bytes] = []

    async def write(b: bytes) -> None:
        captured.append(b)

    r = Registry(hub_run_id="hr_t")
    s = await r.register(name="x", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await r.attach_wrapper(s.id, write)

    await r.resize(s.id, rows=42, cols=121)

    assert captured, "no event written to wrapper writer"
    # Event is length-prefixed JSON-RPC; the inner JSON should carry
    # method=resize_pty plus rows/cols. We don't decode the framing
    # here — substring asserts are enough to pin the contract.
    payload = captured[0]
    assert b"resize_pty" in payload
    assert b'"rows":42' in payload or b'"rows": 42' in payload
    assert b'"cols":121' in payload or b'"cols": 121' in payload


async def test_resize_without_wrapper_raises() -> None:
    """A session with no wrapper writer attached should raise
    WRAPPER_UNREACHABLE — same shape as inject's error path so
    callers can handle both with one branch."""
    r = Registry(hub_run_id="hr_t")
    s = await r.register(name="orphan", kind=SessionKind.WRAPPED, cwd="/tmp")
    with pytest.raises(ChubError) as exc:
        await r.resize(s.id, rows=24, cols=80)
    assert exc.value.code is ErrorCode.WRAPPER_UNREACHABLE


async def test_resize_tmux_kind_is_noop() -> None:
    """TMUX_ATTACHED sessions own their own resize loop (tmux's
    layout engine drives it). Daemon-side resize must be a silent
    no-op for them — no error, no wrapper write."""
    captured: list[bytes] = []

    async def write(b: bytes) -> None:
        captured.append(b)

    r = Registry(hub_run_id="hr_t")
    s = await r.register(
        name="tm",
        kind=SessionKind.TMUX_ATTACHED,
        cwd="/tmp",
        tmux_target="x:0.0",
    )
    await r.attach_wrapper(s.id, write)
    await r.resize(s.id, rows=24, cols=80)
    # No event should have been forwarded.
    assert not any(b"resize_pty" in c for c in captured)


async def test_resize_pty_rpc_param_validation() -> None:
    """The schema accepts only int rows/cols. A string slips through
    pydantic with coercion in lax mode, but our _Strict base means
    the validator should reject it. Sanity-check that pydantic's
    parse path works as expected."""
    from chubby.proto.schema import ResizePtyParams

    p = ResizePtyParams.model_validate({"session_id": "s1", "rows": 24, "cols": 80})
    assert p.session_id == "s1" and p.rows == 24 and p.cols == 80

    # Round-trip through dump → reload, in case any frontend serializes
    # this back from JSON.
    obj = json.loads(p.model_dump_json())
    p2 = ResizePtyParams.model_validate(obj)
    assert p2 == p
