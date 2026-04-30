from pathlib import Path

from chubby.daemon.events import EventLog
from chubby.daemon.persistence import Database
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind


async def test_register_persists_to_db(tmp_path: Path) -> None:
    db = await Database.open(tmp_path / "state.db")
    log = EventLog(tmp_path / "events.ndjson")
    r = Registry(hub_run_id="hr_t", db=db, event_log=log)
    await r.register(name="frontend", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    rows = await db.list_sessions(hub_run_id="hr_t")
    assert len(rows) == 1
    assert any(json_line for json_line in log.replay()
               if json_line.get("event") == "session_added"
               and json_line.get("name") == "frontend")
    await db.close()


async def test_recolor_persists_preferred(tmp_path: Path) -> None:
    db = await Database.open(tmp_path / "state.db")
    r = Registry(hub_run_id="hr_t", db=db, event_log=EventLog(tmp_path / "e.ndjson"))
    s = await r.register(name="x", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await r.recolor(s.id, "#abcdef")
    assert await db.get_preferred_color("x") == "#abcdef"
    await db.close()
