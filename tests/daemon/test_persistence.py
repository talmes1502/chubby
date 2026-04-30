from pathlib import Path

from chubby.daemon.persistence import Database


async def test_open_creates_schema(tmp_path: Path) -> None:
    db = await Database.open(tmp_path / "state.db")
    try:
        cur = await db.conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
        )
        names = [r[0] async for r in cur]
        assert {"sessions", "hub_runs", "name_colors", "transcript_fts"}.issubset(set(names))
    finally:
        await db.close()


async def test_insert_session_round_trip(tmp_path: Path) -> None:
    from chubby.daemon.session import Session, SessionKind, SessionStatus

    db = await Database.open(tmp_path / "state.db")
    s = Session(
        id="s_x", hub_run_id="hr_y", name="frontend", color="#5fafff",
        kind=SessionKind.WRAPPED, cwd="/tmp", created_at=1, last_activity_at=1,
        status=SessionStatus.IDLE, pid=42,
    )
    await db.upsert_session(s)
    rows = await db.list_sessions(hub_run_id="hr_y")
    await db.close()
    assert len(rows) == 1
    assert rows[0].name == "frontend"


async def test_name_color_sticks(tmp_path: Path) -> None:
    db = await Database.open(tmp_path / "state.db")
    await db.set_preferred_color("frontend", "#abcdef")
    color = await db.get_preferred_color("frontend")
    assert color == "#abcdef"
    await db.close()
