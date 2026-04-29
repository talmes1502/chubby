from pathlib import Path

from chub.daemon.events import EventLog


async def test_append_and_replay(tmp_path: Path) -> None:
    log = EventLog(tmp_path / "events.ndjson")
    await log.append({"kind": "session_added", "id": "s_1"})
    await log.append({"kind": "session_renamed", "id": "s_1", "name": "x"})
    events = list(log.replay())
    assert events == [
        {"kind": "session_added", "id": "s_1"},
        {"kind": "session_renamed", "id": "s_1", "name": "x"},
    ]


async def test_replay_skips_corrupt_lines(tmp_path: Path) -> None:
    p = tmp_path / "events.ndjson"
    p.write_text('{"a":1}\nnot json\n{"b":2}\n')
    log = EventLog(p)
    events = list(log.replay())
    assert events == [{"a": 1}, {"b": 2}]
