"""The JSONL tailer broadcasts session_usage_changed for each assistant turn.

Claude's transcript records embed a ``usage`` block with input/output/cache
token counts. The tailer surfaces those counts as a ``session_usage_changed``
event so live TUI clients can display token totals + activity rates without
re-parsing the JSONL themselves.
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


async def test_usage_broadcast_emits_session_usage_changed(tmp_path: Path) -> None:
    transcript = tmp_path / "session.jsonl"
    transcript.write_text(
        json.dumps(
            {
                "type": "assistant",
                "message": {
                    "role": "assistant",
                    "content": [{"type": "text", "text": "hello"}],
                    "usage": {
                        "input_tokens": 1234,
                        "output_tokens": 56,
                        "cache_read_input_tokens": 7890,
                    },
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
            id="s_u",
            hub_run_id="hr_t",
            name="u",
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

        usage_events = [
            (m, p) for m, p in subs.broadcasts if m == "session_usage_changed"
        ]
        assert len(usage_events) == 1
        _, params = usage_events[0]
        assert params["session_id"] == "s_u"
        assert params["input_tokens"] == 1234
        assert params["output_tokens"] == 56
        assert params["cache_read_input_tokens"] == 7890
        assert isinstance(params["ts"], int)
    finally:
        await db.close()
