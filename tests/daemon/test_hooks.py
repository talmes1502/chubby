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
    find_jsonl_for_session,
    find_new_jsonl_for_cwd,
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


def test_find_jsonl_for_session_globs_any_subdir(tmp_path: Path, monkeypatch) -> None:
    """find_jsonl_for_session should locate the file regardless of which
    projects/<subdir>/ Claude wrote it under — no path encoding assumed."""
    fake_root = tmp_path / "projects"
    sub = fake_root / "any-encoded-name-could-go-here"
    sub.mkdir(parents=True)
    (sub / "abc1234.jsonl").write_text("{}\n")

    import chub.daemon.hooks as hooks_mod
    monkeypatch.setattr(hooks_mod, "claude_projects_root", lambda: fake_root)

    found = find_jsonl_for_session("abc1234")
    assert found is not None
    assert found.name == "abc1234.jsonl"


def test_find_new_jsonl_for_cwd_matches_via_cwd_field(
    tmp_path: Path, monkeypatch
) -> None:
    """find_new_jsonl_for_cwd should match by reading the cwd field inside
    the JSONL (encoding-free), not by computing a subdir name."""
    fake_root = tmp_path / "projects"
    sub_a = fake_root / "anything-A"
    sub_b = fake_root / "anything-B"
    sub_a.mkdir(parents=True)
    sub_b.mkdir(parents=True)

    target_cwd = str(tmp_path / "my" / "proj")
    Path(target_cwd).mkdir(parents=True)
    other_cwd = str(tmp_path / "other")
    Path(other_cwd).mkdir(parents=True)

    (sub_a / "match.jsonl").write_text(
        json.dumps({"type": "first", "cwd": target_cwd}) + "\n"
    )
    (sub_b / "wrong.jsonl").write_text(
        json.dumps({"type": "first", "cwd": other_cwd}) + "\n"
    )

    import chub.daemon.hooks as hooks_mod
    monkeypatch.setattr(hooks_mod, "claude_projects_root", lambda: fake_root)

    found = find_new_jsonl_for_cwd(target_cwd, since_ms=0)
    assert found is not None
    assert found.name == "match.jsonl"


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


def test_extract_turn_text_renders_tool_blocks_claude_style() -> None:
    """tool_use blocks become compact one-liners; tool_result blocks
    are dropped entirely (Claude's own UI collapses them)."""
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
    assert "⏺ Read" in out
    # Tool args MUST NOT appear (Claude's UI shows just the tool name).
    assert "/tmp/x.py" not in out
    # Tool results MUST NOT appear at all.
    assert "tool_result" not in out
    assert "6 chars" not in out


def test_extract_turn_text_returns_empty_when_only_tool_results() -> None:
    """A user record with only tool_result blocks (Claude's internal
    delivery of tool output back to the assistant) has no
    user-readable text and should produce empty output so the tailer
    drops the turn instead of rendering '▸ [tool_result …]' rows."""
    msg = {
        "role": "user",
        "content": [
            {"type": "tool_result", "content": "anything"},
        ],
    }
    assert _extract_turn_text(msg) == ""


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


async def test_watch_for_transcript_binds_new_jsonl(tmp_path: Path, monkeypatch) -> None:
    """watch_for_transcript picks up a JSONL written after session creation
    by matching the cwd field inside the file — no path encoding."""
    # Use a real cwd so Path.resolve() comparisons line up.
    cwd_dir = tmp_path / "work"
    cwd_dir.mkdir()
    cwd = str(cwd_dir)

    fake_root = tmp_path / "projects"
    # Put the JSONL in a deliberately-encoded subdir to prove the lookup
    # doesn't depend on any specific encoding scheme.
    sub = fake_root / "whatever-encoded-name"
    sub.mkdir(parents=True)

    import chub.daemon.hooks as hooks_mod
    monkeypatch.setattr(hooks_mod, "claude_projects_root", lambda: fake_root)

    db = await Database.open(tmp_path / "s.db")
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="foo", kind=SessionKind.WRAPPED, cwd=cwd)

    async def writer() -> None:
        await asyncio.sleep(0.05)
        (sub / "abc1234.jsonl").write_text(
            json.dumps({"type": "first", "cwd": cwd}) + "\n"
            + json.dumps(
                {"type": "user", "message": {"role": "user", "content": "hello"}}
            )
            + "\n"
        )

    watch_task = asyncio.create_task(
        watch_for_transcript(reg, s, poll_interval=0.05, timeout=2.0)
    )
    try:
        await writer()
        await asyncio.sleep(0.3)
    finally:
        watch_task.cancel()
        try:
            await watch_task
        except asyncio.CancelledError:
            pass

    bound = await reg.get(s.id)
    assert bound.claude_session_id == "abc1234"
    assert any(m == "session_id_resolved" for m, _ in subs.broadcasts)
    await db.close()
