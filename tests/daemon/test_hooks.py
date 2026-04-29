"""Tests for the JSONL transcript tailer used by readonly sessions."""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

from chub.daemon.hooks import (
    _stringify,
    _tail_jsonl,
    claude_transcript_path,
)
from chub.daemon.persistence import Database
from chub.daemon.registry import Registry
from chub.daemon.session import Session, SessionKind, SessionStatus


def test_claude_transcript_path_encodes_cwd() -> None:
    p = claude_transcript_path("abc1234", "/Users/me/proj")
    assert p.name == "abc1234.jsonl"
    # Encoding replaces "/" with "-"; the Path home is platform-dependent so
    # just check the project-folder element.
    assert p.parent.name == "-Users-me-proj"


def test_stringify_handles_str_list_dict_none() -> None:
    assert _stringify("hello") == "hello"
    assert _stringify(None) == ""
    assert _stringify(["a", "b"]) == "a b"
    assert _stringify({"text": "ok"}) == "ok"
    assert _stringify({"foo": "bar"}) == json.dumps({"foo": "bar"})
    assert _stringify(42) == "42"


async def test_tailer_indexes_new_lines(tmp_path: Path) -> None:
    transcript = tmp_path / "session.jsonl"
    transcript.write_text("")

    db = await Database.open(tmp_path / "s.db")
    reg = Registry(hub_run_id="hr_t", db=db)
    s = Session(
        id="s_x",
        hub_run_id="hr_t",
        name="x",
        color="#abc123",
        kind=SessionKind.READONLY,
        cwd=str(tmp_path),
        created_at=1,
        last_activity_at=1,
        status=SessionStatus.IDLE,
        claude_session_id="abc",
    )
    reg._by_id[s.id] = s

    # Append two messages.
    transcript.write_text(
        json.dumps({"role": "user", "content": "hello"}) + "\n"
        + json.dumps({"role": "assistant", "content": "hi there"}) + "\n"
    )

    await asyncio.wait_for(
        _tail_jsonl(reg, s.id, transcript, stop_after=2),
        timeout=3.0,
    )
    rows = await db.search("hi there")
    await db.close()
    assert len(rows) == 1
    assert rows[0]["session_id"] == "s_x"
    assert rows[0]["role"] == "assistant"


async def test_tailer_skips_blank_and_invalid_lines(tmp_path: Path) -> None:
    transcript = tmp_path / "t.jsonl"
    transcript.write_text(
        "\n"
        "not-json\n"
        + json.dumps({"role": "user", "content": "ping"})
        + "\n"
    )
    db = await Database.open(tmp_path / "s.db")
    reg = Registry(hub_run_id="hr_t", db=db)
    s = Session(
        id="s_y",
        hub_run_id="hr_t",
        name="y",
        color="#abc123",
        kind=SessionKind.READONLY,
        cwd=str(tmp_path),
        created_at=1,
        last_activity_at=1,
        status=SessionStatus.IDLE,
        claude_session_id="zzz",
    )
    reg._by_id[s.id] = s

    await asyncio.wait_for(
        _tail_jsonl(reg, s.id, transcript, stop_after=1),
        timeout=3.0,
    )
    rows = await db.search("ping")
    await db.close()
    assert len(rows) == 1
