"""Tests for the JSONL transcript tailer used by readonly sessions."""

from __future__ import annotations

import asyncio
import json
from pathlib import Path
from typing import Any

from chubby.daemon.hooks import (
    _extract_turn_text,
    _stringify,
    _tail_jsonl,
    find_jsonl_for_session,
    find_new_jsonl_for_cwd,
    session_id_for_pid,
    watch_for_transcript,
)
from chubby.daemon.persistence import Database
from chubby.daemon.registry import Registry
from chubby.daemon.session import Session, SessionKind, SessionStatus


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

    import chubby.daemon.hooks as hooks_mod
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

    import chubby.daemon.hooks as hooks_mod
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


def test_extract_turn_text_strips_tool_blocks_from_prose() -> None:
    """Tool-use blocks no longer leak into the rendered prose body —
    they're carried separately by _extract_turn_payload so the TUI can
    style them as boxed widgets. _extract_turn_text only returns the
    prose, and tool_result blocks are still dropped completely.
    """
    from chubby.daemon.hooks import _extract_turn_payload

    msg = {
        "role": "assistant",
        "content": [
            {"type": "text", "text": "let me check"},
            {
                "type": "tool_use",
                "id": "tu1",
                "name": "Read",
                "input": {"file_path": "/tmp/x.py"},
            },
            {"type": "tool_result", "content": "abcdef"},
        ],
    }
    text, tool_calls = _extract_turn_payload(msg)
    assert "let me check" in text
    assert "⏺ Read" not in text
    assert "/tmp/x.py" not in text
    assert "tool_result" not in text

    assert len(tool_calls) == 1
    assert tool_calls[0]["name"] == "Read"
    assert tool_calls[0]["summary"] == "/tmp/x.py"
    assert tool_calls[0]["id"] == "tu1"


def test_extract_turn_payload_keeps_pure_tool_use_turns() -> None:
    """An assistant record with only tool_use blocks now produces
    ("", [<call>]) — the tailer keeps such turns because the TUI
    renders the tool boxes themselves. The legacy _extract_turn_text
    still returns "" so FTS doesn't index a body for them."""
    from chubby.daemon.hooks import _extract_turn_payload

    msg = {
        "role": "assistant",
        "content": [{
            "type": "tool_use", "id": "tu2",
            "name": "Bash", "input": {"command": "ls -la"},
        }],
    }
    assert _extract_turn_text(msg) == ""
    text, tool_calls = _extract_turn_payload(msg)
    assert text == ""
    assert len(tool_calls) == 1
    assert tool_calls[0]["name"] == "Bash"
    assert tool_calls[0]["summary"] == "ls -la"


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


def test_extract_turn_text_strips_command_caveat() -> None:
    msg = {
        "role": "user",
        "content": "<local-command-caveat>caveat text</local-command-caveat>real text",
    }
    assert _extract_turn_text(msg) == "real text"


def test_extract_turn_text_renders_slash_command_invocation() -> None:
    msg = {
        "role": "user",
        "content": (
            "<command-name>/model</command-name>\n"
            "<command-message>model</command-message>\n"
            "<command-args>claude-opus-4-7</command-args>"
        ),
    }
    assert _extract_turn_text(msg) == "/model claude-opus-4-7"


def test_extract_turn_text_renders_slash_command_no_args() -> None:
    msg = {
        "role": "user",
        "content": "<command-name>/clear</command-name>",
    }
    assert _extract_turn_text(msg) == "/clear"


def test_extract_turn_text_renders_command_stdout_indented() -> None:
    msg = {
        "role": "user",
        "content": (
            "<command-name>/model</command-name>\n"
            "<command-args>opus</command-args>\n"
            "<local-command-stdout>Set model to Opus 4.7\nAlso saved to settings</local-command-stdout>"
        ),
    }
    out = _extract_turn_text(msg)
    assert "/model opus" in out
    assert "  Set model to Opus 4.7" in out
    assert "  Also saved to settings" in out


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

    # The tailer broadcasts both transcript_message events AND status
    # transitions: user-turn → THINKING (so the rail spinner kicks in
    # while claude responds), then assistant-turn → IDLE (so it falls
    # back to the post-response state until Stop fires AWAITING_USER).
    methods = [m for m, _ in subs.broadcasts]
    assert methods == [
        "transcript_message",
        "session_status_changed",
        "transcript_message",
        "session_status_changed",
    ]
    transcripts = [p for m, p in subs.broadcasts if m == "transcript_message"]
    status_events = [p for m, p in subs.broadcasts if m == "session_status_changed"]
    assert transcripts[0]["role"] == "user"
    assert transcripts[0]["text"] == "hello"
    assert transcripts[0]["session_id"] == "s_x"
    assert transcripts[1]["role"] == "assistant"
    assert transcripts[1]["text"] == "hi there"
    assert isinstance(transcripts[1]["ts"], int)
    assert status_events[0]["status"] == "thinking"
    assert status_events[1]["status"] == "idle"


async def test_tailer_flips_to_thinking_on_user_turn_from_awaiting(
    tmp_path: Path,
) -> None:
    """Regression: post-PTY-pivot, the rail glyph stayed ⚡ (awaiting)
    while claude was actively responding because inject_raw can't tell
    a submit keystroke apart from any other byte. The JSONL tailer
    must pick up the slack — when a new user-role record lands and the
    session is currently AWAITING_USER (or IDLE), flip THINKING so the
    rail spinner reflects what claude is doing."""
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
        status=SessionStatus.AWAITING_USER,
        claude_session_id="abc",
    )
    reg._by_id[s.id] = s
    transcript.write_text(
        json.dumps(
            {
                "type": "user",
                "message": {"role": "user", "content": "next prompt"},
            }
        )
        + "\n"
    )
    await asyncio.wait_for(
        _tail_jsonl(reg, s.id, transcript, stop_after=1), timeout=3.0
    )
    await db.close()
    cur = await reg.get(s.id)
    assert cur.status is SessionStatus.THINKING, (
        f"AWAITING_USER → user-turn should flip THINKING, got {cur.status}"
    )


async def test_tailer_does_not_flip_dead_session_to_thinking(
    tmp_path: Path,
) -> None:
    """A DEAD session that somehow has a JSONL update (resumed
    externally, etc.) must NOT come back to THINKING. Dead means dead
    until explicit respawn."""
    transcript = tmp_path / "session.jsonl"
    transcript.write_text("")
    db = await Database.open(tmp_path / "s.db")
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
    s = Session(
        id="s_dead",
        hub_run_id="hr_t",
        name="x",
        color="#abc123",
        kind=SessionKind.READONLY,
        cwd=str(tmp_path),
        created_at=1,
        last_activity_at=1,
        status=SessionStatus.DEAD,
        claude_session_id="abc",
    )
    reg._by_id[s.id] = s
    transcript.write_text(
        json.dumps(
            {"type": "user", "message": {"role": "user", "content": "x"}}
        )
        + "\n"
    )
    await asyncio.wait_for(
        _tail_jsonl(reg, s.id, transcript, stop_after=1), timeout=3.0
    )
    await db.close()
    cur = await reg.get(s.id)
    assert cur.status is SessionStatus.DEAD, (
        f"DEAD must stay DEAD; got {cur.status}"
    )


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
    by matching the cwd field inside the file — no path encoding.

    Falls back to the mtime/cwd heuristic when no ``claude_pid`` is given.
    """
    # Use a real cwd so Path.resolve() comparisons line up.
    cwd_dir = tmp_path / "work"
    cwd_dir.mkdir()
    cwd = str(cwd_dir)

    fake_root = tmp_path / "projects"
    # Put the JSONL in a deliberately-encoded subdir to prove the lookup
    # doesn't depend on any specific encoding scheme.
    sub = fake_root / "whatever-encoded-name"
    sub.mkdir(parents=True)

    import chubby.daemon.hooks as hooks_mod
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


def test_session_id_for_pid_reads_mapping(tmp_path: Path, monkeypatch) -> None:
    """session_id_for_pid resolves the pid → sessionId mapping that Claude
    writes to ``~/.claude/sessions/<pid>.json``."""
    fake_dir = tmp_path / "sessions"
    fake_dir.mkdir()
    (fake_dir / "12345.json").write_text(
        json.dumps(
            {
                "pid": 12345,
                "sessionId": "abc-1234-uuid",
                "cwd": "/some/cwd",
            }
        )
    )

    import chubby.daemon.hooks as hooks_mod
    monkeypatch.setattr(hooks_mod, "claude_sessions_dir", lambda: fake_dir)

    assert session_id_for_pid(12345) == "abc-1234-uuid"


def test_session_id_for_pid_missing_returns_none(tmp_path: Path, monkeypatch) -> None:
    """A missing pid mapping returns None; caller is expected to retry."""
    fake_dir = tmp_path / "sessions"
    fake_dir.mkdir()

    import chubby.daemon.hooks as hooks_mod
    monkeypatch.setattr(hooks_mod, "claude_sessions_dir", lambda: fake_dir)

    assert session_id_for_pid(99999) is None

    # Malformed JSON: also None (no crash).
    (fake_dir / "55555.json").write_text("{not json")
    assert session_id_for_pid(55555) is None

    # Missing sessionId field: also None.
    (fake_dir / "66666.json").write_text(json.dumps({"pid": 66666}))
    assert session_id_for_pid(66666) is None


async def test_watch_for_transcript_binds_via_claude_pid(
    tmp_path: Path, monkeypatch
) -> None:
    """Happy path with claude_pid: the pid mapping resolves the sessionId,
    we glob the JSONL by id (encoding-free), bind, and start tailing —
    even if a stale unrelated JSONL with a more recent mtime exists in
    the same cwd. That stale file is what would trip the mtime
    heuristic; the pid-keyed lookup must ignore it.
    """
    cwd_dir = tmp_path / "work"
    cwd_dir.mkdir()
    cwd = str(cwd_dir)

    fake_projects = tmp_path / "projects"
    sub = fake_projects / "encoded-cwd"
    sub.mkdir(parents=True)
    fake_sessions = tmp_path / "sessions"
    fake_sessions.mkdir()

    # A stale JSONL belonging to an UNRELATED active Claude in the same
    # cwd — newer mtime, would beat the new session in the legacy
    # mtime heuristic. We must NOT pick this one.
    (sub / "stale-other.jsonl").write_text(
        json.dumps({"type": "first", "cwd": cwd}) + "\n"
    )

    # The "real" JSONL for the wrapped session, with a known UUID we
    # surface via the pid mapping.
    real_id = "abc1234"
    (sub / f"{real_id}.jsonl").write_text(
        json.dumps({"type": "first", "cwd": cwd}) + "\n"
        + json.dumps(
            {"type": "user", "message": {"role": "user", "content": "hello"}}
        )
        + "\n"
    )
    # Claude's per-pid mapping file — the precise binding we trust.
    claude_pid = 424242
    (fake_sessions / f"{claude_pid}.json").write_text(
        json.dumps({"pid": claude_pid, "sessionId": real_id, "cwd": cwd})
    )

    import chubby.daemon.hooks as hooks_mod
    monkeypatch.setattr(hooks_mod, "claude_projects_root", lambda: fake_projects)
    monkeypatch.setattr(hooks_mod, "claude_sessions_dir", lambda: fake_sessions)

    db = await Database.open(tmp_path / "s.db")
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="foo", kind=SessionKind.WRAPPED, cwd=cwd)

    watch_task = asyncio.create_task(
        watch_for_transcript(
            reg, s, claude_pid=claude_pid, poll_interval=0.05, timeout=2.0
        )
    )
    try:
        await asyncio.sleep(0.3)
    finally:
        watch_task.cancel()
        try:
            await watch_task
        except asyncio.CancelledError:
            pass

    bound = await reg.get(s.id)
    assert bound.claude_session_id == real_id
    assert any(m == "session_id_resolved" for m, _ in subs.broadcasts)
    await db.close()
