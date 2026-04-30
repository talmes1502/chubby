from pathlib import Path

from chubby.daemon.persistence import Database


async def test_insert_and_search(tmp_path: Path) -> None:
    db = await Database.open(tmp_path / "s.db")
    await db.insert_message(
        session_id="s_1",
        hub_run_id="hr_1",
        ts=1,
        role="assistant",
        content="reading src/auth/Modal.tsx",
    )
    await db.insert_message(
        session_id="s_1",
        hub_run_id="hr_1",
        ts=2,
        role="user",
        content="DELAYED_QUEUE_FULL error appeared",
    )
    rows = await db.search("DELAYED_QUEUE_FULL")
    await db.close()
    assert len(rows) == 1
    assert rows[0]["session_id"] == "s_1"
