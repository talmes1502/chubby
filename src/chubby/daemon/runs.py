"""Hub-run lifecycle: allocate run id, materialize run dir, write run row, expose event log."""

from __future__ import annotations

import socket
from dataclasses import dataclass
from pathlib import Path

from chubby.daemon.clock import now_ms
from chubby.daemon.events import EventLog
from chubby.daemon.ids import new_hub_run_id
from chubby.daemon.paths import runs_dir
from chubby.daemon.persistence import Database


@dataclass
class HubRun:
    id: str
    started_at: int
    dir: Path
    event_log: EventLog


async def start_run(db: Database, *, resumed_from: str | None = None) -> HubRun:
    run_id = new_hub_run_id()
    d = runs_dir() / run_id
    d.mkdir(parents=True, exist_ok=True)
    (d / "logs").mkdir(exist_ok=True)
    await db.insert_hub_run(run_id, hostname=socket.gethostname(), resumed_from=resumed_from)
    return HubRun(id=run_id, started_at=now_ms(), dir=d, event_log=EventLog(d / "events.ndjson"))


async def end_run(db: Database, run_id: str) -> None:
    await db.end_hub_run(run_id)


async def resolve_resume(db: Database, target: str) -> str | None:
    """Translate a `--resume` target (run-id or ``last``) to a concrete run id.

    Returns ``None`` if ``target == "last"`` and no prior runs exist.
    """
    if target == "last":
        runs = await db.list_hub_runs()
        if not runs:
            return None
        return str(runs[0]["id"])
    return target
