"""Rebuild state.db from events.ndjson — used when SQLite is missing or corrupt."""

from __future__ import annotations

from chub.daemon.events import EventLog
from chub.daemon.persistence import Database
from chub.daemon.session import Session, SessionKind, SessionStatus


async def rebuild_from_events(
    db: Database, log: EventLog, *, hub_run_id: str
) -> None:
    sessions: dict[str, Session] = {}
    for ev in log.replay():
        e = ev.get("event")
        sid = ev.get("id")
        if not isinstance(sid, str):
            continue
        if e == "session_added":
            sessions[sid] = Session(
                id=sid,
                hub_run_id=ev["hub_run_id"],
                name=ev["name"],
                color=ev["color"],
                kind=SessionKind(ev["kind"]),
                cwd=ev["cwd"],
                created_at=ev["created_at"],
                last_activity_at=ev["last_activity_at"],
                status=SessionStatus(ev["status"]),
                pid=ev.get("pid"),
                claude_session_id=ev.get("claude_session_id"),
                tmux_target=ev.get("tmux_target"),
                tags=list(ev.get("tags") or []),
                ended_at=ev.get("ended_at"),
            )
        elif e == "session_renamed" and sid in sessions:
            sessions[sid].name = ev["name"]
        elif e == "session_recolored" and sid in sessions:
            sessions[sid].color = ev["color"]
        elif e == "session_status_changed" and sid in sessions:
            sessions[sid].status = SessionStatus(ev["status"])
        elif e == "session_tagged" and sid in sessions:
            sessions[sid].tags = list(ev.get("tags") or [])
    for s in sessions.values():
        await db.upsert_session(s)
        await db.set_preferred_color(s.name, s.color)
