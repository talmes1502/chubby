import asyncio
from pathlib import Path

from chubby.daemon.events import EventLog
from chubby.daemon.logs import LogWriter
from chubby.daemon.persistence import Database
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind


async def test_record_chunk_writes_log_and_indexes_fts(tmp_path: Path) -> None:
    db = await Database.open(tmp_path / "s.db")
    event_log = EventLog(tmp_path / "events.ndjson")
    reg = Registry(hub_run_id="hr_1", db=db, event_log=event_log)
    s = await reg.register(name="frontend", kind=SessionKind.WRAPPED, cwd="/tmp")

    logs_dir = tmp_path / "logs"
    writer = LogWriter(logs_dir, color=s.color, session_name=s.name)
    await reg.attach_log_writer(s.id, writer)

    payload = b"DELAYED_QUEUE_FULL appeared in run\n"
    await reg.record_chunk(s.id, payload, role="raw")

    # Wait for the debounced flush (200ms) to fire.
    await asyncio.sleep(0.4)

    # Log file written.
    log_path = logs_dir / "frontend.log"
    assert log_path.exists()
    assert payload in log_path.read_bytes()

    # FTS index populated.
    rows = await db.search("DELAYED_QUEUE_FULL")
    assert len(rows) >= 1
    assert rows[0]["session_id"] == s.id

    await writer.close()
    await db.close()
