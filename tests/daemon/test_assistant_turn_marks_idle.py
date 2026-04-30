"""The JSONL tailer flips THINKING → IDLE on each assistant turn.

Pairs with the inject-side change in main.py: inject sets THINKING,
the next assistant message in the transcript flips it back. The guard
in _tail_jsonl ensures we don't override AWAITING_USER (set by the
Stop hook on readonly sessions) or step on a DEAD session.
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path
from typing import Any

from chubby.daemon.hooks import _tail_jsonl
from chubby.daemon.persistence import Database
from chubby.daemon.registry import Registry
from chubby.daemon.session import Session, SessionKind, SessionStatus


class _FakeSubs:
    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, event_method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((event_method, params))


async def test_assistant_turn_flips_thinking_to_idle(tmp_path: Path) -> None:
    transcript = tmp_path / "session.jsonl"
    transcript.write_text(
        json.dumps(
            {
                "type": "assistant",
                "message": {
                    "role": "assistant",
                    "content": [{"type": "text", "text": "done"}],
                },
            }
        )
        + "\n"
    )

    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = Session(
            id="s_t",
            hub_run_id="hr_t",
            name="t",
            color="#abc123",
            kind=SessionKind.WRAPPED,
            cwd=str(tmp_path),
            created_at=1,
            last_activity_at=1,
            status=SessionStatus.THINKING,
            claude_session_id="abc",
        )
        reg._by_id[s.id] = s

        await asyncio.wait_for(
            _tail_jsonl(reg, s.id, transcript, stop_after=1),
            timeout=3.0,
        )
        cur = await reg.get(s.id)
        assert cur.status is SessionStatus.IDLE
    finally:
        await db.close()


async def test_assistant_turn_does_not_override_awaiting_user(tmp_path: Path) -> None:
    """AWAITING_USER (set by readonly Stop hook) must not be flipped to
    IDLE by the assistant-turn handler — only THINKING is reset."""
    transcript = tmp_path / "session.jsonl"
    transcript.write_text(
        json.dumps(
            {
                "type": "assistant",
                "message": {
                    "role": "assistant",
                    "content": [{"type": "text", "text": "ack"}],
                },
            }
        )
        + "\n"
    )

    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = Session(
            id="s_a",
            hub_run_id="hr_t",
            name="a",
            color="#abc123",
            kind=SessionKind.READONLY,
            cwd=str(tmp_path),
            created_at=1,
            last_activity_at=1,
            status=SessionStatus.AWAITING_USER,
            claude_session_id="def",
        )
        reg._by_id[s.id] = s

        await asyncio.wait_for(
            _tail_jsonl(reg, s.id, transcript, stop_after=1),
            timeout=3.0,
        )
        cur = await reg.get(s.id)
        # Status MUST remain AWAITING_USER.
        assert cur.status is SessionStatus.AWAITING_USER
    finally:
        await db.close()
