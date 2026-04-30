"""Tests for rebuild_from_events."""

from __future__ import annotations

from pathlib import Path

from chubby.daemon.events import EventLog
from chubby.daemon.persistence import Database
from chubby.daemon.rebuild import rebuild_from_events


async def test_rebuild_replays_session_added(tmp_path: Path) -> None:
    log = EventLog(tmp_path / "events.ndjson")
    await log.append(
        {
            "event": "session_added",
            "id": "s_a",
            "hub_run_id": "hr_t",
            "name": "frontend",
            "color": "#5fafff",
            "kind": "wrapped",
            "cwd": "/tmp",
            "pid": 1,
            "claude_session_id": None,
            "tmux_target": None,
            "tags": [],
            "status": "idle",
            "created_at": 1,
            "last_activity_at": 1,
            "ended_at": None,
        }
    )
    await log.append({"event": "session_renamed", "id": "s_a", "name": "ui"})
    await log.append({"event": "session_recolored", "id": "s_a", "color": "#abcdef"})

    db = await Database.open(tmp_path / "state.db")
    await rebuild_from_events(db, log, hub_run_id="hr_t")
    rows = await db.list_sessions(hub_run_id="hr_t")
    await db.close()
    assert len(rows) == 1
    assert rows[0].name == "ui"
    assert rows[0].color == "#abcdef"
