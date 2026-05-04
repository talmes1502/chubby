"""Tests for ``release_session`` — the daemon side of the TUI's
``/detach`` slash command. The RPC must:

1. Capture the focused session's ``claude_session_id`` and ``cwd``.
2. Push a ``shutdown`` event to the wrapper's writer (so it SIGTERMs
   claude and exits without a --resume retry).
3. Mark the session DEAD, detach the wrapper, and remove it from the
   in-memory registry (so it disappears from ``list_sessions``).
4. Reject sessions that don't have a bound ``claude_session_id`` yet
   with INVALID_PAYLOAD and a "wait a moment and retry" hint.
"""

from __future__ import annotations

import json

import pytest

from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind, SessionStatus
from chubby.proto.errors import ChubError, ErrorCode
from chubby.proto.rpc import Event, encode_message
from chubby.proto.schema import ReleaseSessionParams, ReleaseSessionResult


async def _run_release(reg: Registry, sid: str) -> dict:
    """Replicate the ``release_session`` handler logic against ``reg``.

    We don't go through the RPC envelope here — the handler closes
    over the registry, and reaching it requires booting chubbyd. The
    mutation is simple and self-contained, so a direct in-process
    invocation is enough to exercise the contract.
    """
    p = ReleaseSessionParams.model_validate({"id": sid})
    s = await reg.get(p.id)
    if not s.claude_session_id:
        raise ChubError(
            ErrorCode.INVALID_PAYLOAD,
            "session has no bound claude session id yet — wait a moment and retry",
        )
    result = ReleaseSessionResult(
        claude_session_id=s.claude_session_id,
        cwd=s.cwd,
    )
    if s.kind in (SessionKind.WRAPPED, SessionKind.SPAWNED):
        write = reg._wrapper_writers.get(s.id)
        if write is not None:
            try:
                await write(
                    encode_message(
                        Event(method="shutdown", params={"session_id": s.id})
                    )
                )
            except Exception:
                pass
    elif s.kind is SessionKind.TMUX_ATTACHED:
        stop = reg._tmux_stops.get(s.id)
        if stop is not None:
            stop.set()
    await reg.update_status(s.id, SessionStatus.DEAD)
    await reg.detach_wrapper(s.id)
    await reg.remove_session(s.id)
    return result.model_dump()


async def test_release_wrapped_session_emits_shutdown_and_removes() -> None:
    """End-to-end registry exercise: register a wrapped session, attach
    a fake wrapper writer, manually bind a claude_session_id (mimicking
    what watch_for_transcript would do), then run release_session. The
    shutdown event must reach the wrapper writer; the session must
    disappear from list_all and ``get`` must raise SESSION_NOT_FOUND."""
    reg = Registry(hub_run_id="hr_t")
    s = await reg.register(
        name="api",
        kind=SessionKind.WRAPPED,
        cwd="/tmp/proj",
        pid=99999,
    )
    await reg.set_claude_session_id(s.id, "11111111-1111-1111-1111-111111111111")

    captured: list[bytes] = []

    async def fake_write(b: bytes) -> None:
        captured.append(b)

    await reg.attach_wrapper(s.id, fake_write)

    out = await _run_release(reg, s.id)

    # Result carries the captured fields.
    assert out["claude_session_id"] == "11111111-1111-1111-1111-111111111111"
    assert out["cwd"] == "/tmp/proj"

    # Wrapper writer received a shutdown event.
    assert captured, "no bytes pushed to wrapper writer"
    body = json.loads(captured[0])
    assert body["method"] == "shutdown"
    assert body["params"]["session_id"] == s.id

    # Session removed from in-memory registry.
    live = await reg.list_all()
    assert all(x.id != s.id for x in live)
    with pytest.raises(ChubError) as exc:
        await reg.get(s.id)
    assert exc.value.code is ErrorCode.SESSION_NOT_FOUND

    # Wrapper writer detached.
    assert s.id not in reg._wrapper_writers


async def test_release_without_claude_session_id_errors() -> None:
    """A session that hasn't bound its claude_session_id yet (newly
    spawned, JSONL not yet linked) must surface INVALID_PAYLOAD with
    the documented "wait a moment and retry" hint so the TUI can
    show a clear message."""
    reg = Registry(hub_run_id="hr_t")
    s = await reg.register(
        name="fresh",
        kind=SessionKind.WRAPPED,
        cwd="/tmp/proj",
        pid=88888,
    )
    # Note: no set_claude_session_id call — it stays None.

    with pytest.raises(ChubError) as exc:
        await _run_release(reg, s.id)
    assert exc.value.code is ErrorCode.INVALID_PAYLOAD
    assert "wait a moment" in exc.value.message
    # Session must NOT have been removed — the user can retry.
    again = await reg.get(s.id)
    assert again.id == s.id


async def test_release_unknown_session_errors() -> None:
    """Releasing an unknown id surfaces SESSION_NOT_FOUND so the TUI
    can drop the toast cleanly."""
    reg = Registry(hub_run_id="hr_t")
    with pytest.raises(ChubError) as exc:
        await _run_release(reg, "no-such-id")
    assert exc.value.code is ErrorCode.SESSION_NOT_FOUND


async def test_release_with_no_attached_wrapper_still_removes() -> None:
    """If the wrapper writer is already gone (transport closed, wrapper
    crashed), release still cleans up the registry entry — we don't
    leak DEAD-but-listed entries waiting on a wrapper that will never
    come back."""
    reg = Registry(hub_run_id="hr_t")
    s = await reg.register(
        name="orphan",
        kind=SessionKind.WRAPPED,
        cwd="/tmp/proj",
        pid=77777,
    )
    await reg.set_claude_session_id(
        s.id, "22222222-2222-2222-2222-222222222222"
    )
    # No attach_wrapper — writers map empty for this id.
    assert s.id not in reg._wrapper_writers

    out = await _run_release(reg, s.id)
    assert out["claude_session_id"] == "22222222-2222-2222-2222-222222222222"

    with pytest.raises(ChubError) as exc:
        await reg.get(s.id)
    assert exc.value.code is ErrorCode.SESSION_NOT_FOUND


async def test_release_completes_chain_when_a_step_raises(
    tmp_path: pytest.TempPathFactory,
) -> None:
    """Regression: release_session must remove the session from the
    in-memory registry even if a cleanup step raises mid-chain.

    Real-world failure mode: the session ends up DEAD-but-still-listed
    because something between ``update_status`` and ``remove_session``
    raised. The user could see the row stuck in the rail with no way
    to clear it short of restarting the daemon.

    We monkeypatch ``detach_wrapper`` to raise, then call the actual
    handler (via _build_registry) and assert the session is gone from
    list_all anyway.
    """
    from pathlib import Path
    from chubby.daemon.events import EventLog
    from chubby.daemon.handlers import CallContext
    from chubby.daemon.main import _build_registry
    from chubby.daemon.persistence import Database
    from chubby.daemon.runs import HubRun
    from chubby.daemon.subscriptions import SubscriptionHub

    db_path = Path(str(tmp_path)) / "s.db"  # type: ignore[arg-type]
    db = await Database.open(db_path)
    try:
        subs = SubscriptionHub()
        run_dir = db_path.parent / "run"
        run_dir.mkdir(exist_ok=True)
        run = HubRun(
            id="hr_t",
            started_at=0,
            dir=run_dir,
            event_log=EventLog(run_dir / "events.ndjson"),
        )
        reg = Registry(hub_run_id=run.id, db=db, subs=subs)
        s = await reg.register(
            name="brittle",
            kind=SessionKind.WRAPPED,
            cwd=str(run_dir),
            pid=12345,
        )
        await reg.set_claude_session_id(
            s.id, "33333333-3333-3333-3333-333333333333"
        )

        # Make detach_wrapper blow up — it sits between update_status
        # and remove_session in release_session, and is the kind of
        # thing that could plausibly throw if a writer's transport
        # closed at exactly the wrong instant.
        original_detach = reg.detach_wrapper

        async def _exploding_detach(sid: str) -> None:
            raise RuntimeError("simulated transport hiccup")

        reg.detach_wrapper = _exploding_detach  # type: ignore[method-assign]

        async def _noop_write(_b: bytes) -> None:
            return None

        ctx = CallContext(
            connection_id=0, write=_noop_write, on_close=lambda _cb: None
        )

        handlers = _build_registry(reg, run, db, subs)
        # The RPC call must not raise — the user committed to release;
        # cleanup errors are best-effort.
        result = await handlers.invoke(
            "release_session", {"id": s.id}, ctx
        )
        assert result["claude_session_id"] == "33333333-3333-3333-3333-333333333333"

        # Restore so list_all doesn't accidentally re-trip the patch.
        reg.detach_wrapper = original_detach  # type: ignore[method-assign]

        # The bug was that the session lingered with status=DEAD; the
        # fix removes it from the registry.
        live = await reg.list_all()
        assert all(x.id != s.id for x in live), (
            "release_session must remove the session even if a step raised"
        )
    finally:
        await db.close()


async def test_remove_session_broadcasts_session_removed() -> None:
    """``Registry.remove_session`` (the helper underneath release)
    must broadcast a ``session_removed`` event so subscribers (the TUI)
    can update without polling list_sessions."""
    from chubby.daemon.subscriptions import SubscriptionHub

    subs = SubscriptionHub()
    reg = Registry(hub_run_id="hr_t", subs=subs)
    s = await reg.register(
        name="x",
        kind=SessionKind.WRAPPED,
        cwd="/tmp",
        pid=1,
    )

    captured: list[dict] = []

    async def fake_writer(b: bytes) -> None:
        # SubscriptionHub.broadcast wraps everything in a top-level
        # ``event`` envelope; the actual broadcast method is in
        # params.event_method.
        body = json.loads(b)
        captured.append(body.get("params") or {})

    await subs.subscribe(fake_writer)
    await reg.remove_session(s.id)

    methods = [c.get("event_method") for c in captured]
    assert "session_removed" in methods, methods
    # And the per-event params include the session id.
    for c in captured:
        if c.get("event_method") == "session_removed":
            assert (c.get("event_params") or {}).get("id") == s.id
            break
