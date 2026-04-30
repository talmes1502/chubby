"""Tests for the JSONL transcript tailer used by readonly sessions."""

from __future__ import annotations

import asyncio
import json
from pathlib import Path
from typing import Any

from chub.daemon.hooks import (
    _extract_turn_text,
    _stringify,
    _tail_jsonl,
    claude_transcript_path,
    watch_for_transcript,
)
from chub.daemon.persistence import Database
from chub.daemon.registry import Registry
from chub.daemon.session import Session, SessionKind, SessionStatus


class _FakeSubs:
    """Capture broadcast(method, params) calls for assertions."""

    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, event_method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((event_method, params))


def test_claude_transcript_path_encodes_cwd() -> None:
    p = claude_transcript_path("abc1234", "/Users/me/proj")
    assert p.name == "abc1234.jsonl"
    # Encoding replaces "/" with "-" and strips the leading dash; the Path
    # home is platform-dependent so just check the project-folder element.
    assert p.parent.name == "Users-me-proj"


def test_stringify_handles_str_list_dict_none() -> None:
    assert _stringify("hello") == "hello"
    assert _stringify(None) == ""
    assert _stringify(["a", "b"]) == "a b"
    assert _stringify({"text": "ok"}) == "ok"
    assert _stringify({"foo": "bar"}) == json.dumps({"foo": "bar"})
    assert _stringify(42) == "42"


def test_extract_turn_text_plain_string() -> None:
    assert _extract_turn_text({"role": "user", "content": "hi"}) == "hi"


def test_extract_turn_text_text_blocks() -> None:
    msg = {
        "role": "assistant",
        "content": [
            {"type": "text", "text": "first part"},
            {"type": "text", "text": "second part"},
        ],
    }
    assert _extract_turn_text(msg) == "first part\nsecond part"


def test_extract_turn_text_renders_tool_blocks_compactly() -> None:
    msg = {
        "role": "assistant",
        "content": [
            {"type": "text", "text": "let me check"},
            {
                "type": "tool_use",
                "name": "Read",
                "input": {"file_path": "/tmp/x.py"},
            },
            {
                "type": "tool_result",
                "content": "abcdef",
            },
        ],
    }
    out = _extract_turn_text(msg)
    assert "let me check" in out
    assert "[tool_use: Read(/tmp/x.py)]" in out
    assert "[tool_result: 6 chars]" in out


async def test_tailer_indexes_new_lines_and_broadcasts(tmp_path: Path) -> None:
    transcript = tmp_path / "session.jsonl"
    transcript.write_text("")

    db = await Database.open(tmp_path / "s.db")
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
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

    # Append two messages — one plain string content, one with text blocks.
    transcript.write_text(
        json.dumps(
            {
                "type": "user",
                "message": {"role": "user", "content": "hello"},
            }
        )
        + "\n"
        + json.dumps(
            {
                "type": "assistant",
                "message": {
                    "role": "assistant",
                    "content": [{"type": "text", "text": "hi there"}],
                },
            }
        )
        + "\n"
    )

    await asyncio.wait_for(
        _tail_jsonl(reg, s.id, transcript, stop_after=2),
        timeout=3.0,
    )
    rows = await db.search("hi there")
    await db.close()

    # FTS got both turns.
    assert len(rows) == 1
    assert rows[0]["session_id"] == "s_x"
    assert rows[0]["role"] == "assistant"

    # And both turns broadcast as transcript_message events.
    assert len(subs.broadcasts) == 2
    methods = {m for m, _ in subs.broadcasts}
    assert methods == {"transcript_message"}
    payloads = [p for _, p in subs.broadcasts]
    assert payloads[0]["role"] == "user"
    assert payloads[0]["text"] == "hello"
    assert payloads[0]["session_id"] == "s_x"
    assert payloads[1]["role"] == "assistant"
    assert payloads[1]["text"] == "hi there"
    assert isinstance(payloads[1]["ts"], int)


async def test_tailer_skips_blank_invalid_and_non_turn_records(tmp_path: Path) -> None:
    transcript = tmp_path / "t.jsonl"
    transcript.write_text(
        "\n"
        "not-json\n"
        # Non-conversation record: should be skipped.
        + json.dumps({"type": "summary", "value": "ignored"})
        + "\n"
        + json.dumps(
            {
                "type": "user",
                "message": {"role": "user", "content": "ping"},
            }
        )
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


async def test_watch_for_transcript_binds_new_jsonl(tmp_path: Path) -> None:
    """watch_for_transcript should pick up a JSONL written after session creation
    and call set_claude_session_id."""
    # Fake home — point projects at tmp_path/projects/<encoded>.
    cwd = "/proj/foo"
    encoded = "proj-foo"
    proj_dir = tmp_path / "projects" / encoded
    proj_dir.mkdir(parents=True)

    db = await Database.open(tmp_path / "s.db")
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
    s = await reg.register(
        name="foo", kind=SessionKind.WRAPPED, cwd=cwd
    )

    # Monkey-patch claude_projects_dir to point at our tmp dir.
    import chub.daemon.hooks as hooks_mod

    orig = hooks_mod.claude_projects_dir
    hooks_mod.claude_projects_dir = lambda c: proj_dir  # type: ignore[assignment]
    try:
        # Fire the watcher in the background; after a short delay write a
        # JSONL — the watcher should detect it and call set_claude_session_id.
        async def writer() -> None:
            await asyncio.sleep(0.05)
            (proj_dir / "abc1234.jsonl").write_text(
                json.dumps(
                    {
                        "type": "user",
                        "message": {"role": "user", "content": "hello"},
                    }
                )
                + "\n"
            )

        # Run the watcher with stop_after=1 inside _tail_jsonl by giving a
        # very short timeout window — but we *do* want it to find the file
        # and invoke set_claude_session_id, so use a short poll interval and
        # just cancel after a moment.
        watch_task = asyncio.create_task(
            watch_for_transcript(reg, s, poll_interval=0.05, timeout=2.0)
        )
        await writer()
        # Give the watcher a moment to detect, bind, then start tailing.
        await asyncio.sleep(0.3)
        watch_task.cancel()
        try:
            await watch_task
        except asyncio.CancelledError:
            pass

        bound = await reg.get(s.id)
        assert bound.claude_session_id == "abc1234"
        # session_id_resolved should have been broadcast.
        assert any(
            m == "session_id_resolved" for m, _ in subs.broadcasts
        )
    finally:
        hooks_mod.claude_projects_dir = orig  # type: ignore[assignment]
        await db.close()
