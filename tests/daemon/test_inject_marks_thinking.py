"""The inject RPC marks the session THINKING after pushing to the wrapper.

This is the visual signal half of the thinking indicator: the daemon
flips status to ``thinking`` immediately on inject, and ``_tail_jsonl``
flips it back to ``idle`` once Claude's next assistant turn lands in
the JSONL transcript.
"""

from __future__ import annotations

import base64
from pathlib import Path

from chub.daemon.events import EventLog
from chub.daemon.handlers import CallContext
from chub.daemon.main import _build_registry
from chub.daemon.persistence import Database
from chub.daemon.registry import Registry
from chub.daemon.runs import HubRun
from chub.daemon.session import SessionKind, SessionStatus
from chub.daemon.subscriptions import SubscriptionHub


async def _noop_write(_b: bytes) -> None:  # pragma: no cover - unused
    return None


def _ctx() -> CallContext:
    return CallContext(connection_id=0, write=_noop_write, on_close=lambda _cb: None)


async def test_inject_marks_session_thinking(tmp_path: Path) -> None:
    """After a successful inject, the session status flips to THINKING.

    A fake wrapper-writer is attached so Registry.inject doesn't raise
    WRAPPER_UNREACHABLE; we don't care about the bytes here, only the
    status mutation.
    """
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = SubscriptionHub()
        run_dir = tmp_path / "run"
        run_dir.mkdir(exist_ok=True)
        run = HubRun(
            id="hr_t",
            started_at=0,
            dir=run_dir,
            event_log=EventLog(run_dir / "events.ndjson"),
        )
        reg = Registry(hub_run_id=run.id, db=db, subs=subs)
        s = await reg.register(name="x", kind=SessionKind.WRAPPED, cwd=str(tmp_path))
        # Stand in for the real wrapper transport.
        captured: list[bytes] = []

        async def fake_write(b: bytes) -> None:
            captured.append(b)

        await reg.attach_wrapper(s.id, fake_write)

        handlers = _build_registry(reg, run, db, subs)

        # Sanity: starts idle.
        before = await reg.get(s.id)
        assert before.status is SessionStatus.IDLE

        await handlers.invoke(
            "inject",
            {"session_id": s.id, "payload_b64": base64.b64encode(b"hi\n").decode()},
            _ctx(),
        )

        after = await reg.get(s.id)
        assert after.status is SessionStatus.THINKING
        # And the inject bytes did reach the wrapper.
        assert captured
    finally:
        await db.close()
