"""Tests for the first-user-message preview helper + Registry hook
that surfaces it to the TUI quick switcher / rail.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from chubby.daemon.hooks import peek_first_user_message
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind


def _write_jsonl(path: Path, records: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps(r) + "\n")


# ---------- peek_first_user_message ----------


def test_peek_returns_first_user_text(tmp_path: Path) -> None:
    """Standard claude JSONL shape: summary line, then a user turn,
    then more turns. The helper should pull the first user turn and
    return its text."""
    p = tmp_path / "x.jsonl"
    _write_jsonl(
        p,
        [
            {"type": "summary", "summary": "x"},
            {"type": "user", "message": {"role": "user", "content": "hello world"}},
            {"type": "assistant", "message": {"content": "hi"}},
        ],
    )
    assert peek_first_user_message(p) == "hello world"


def test_peek_handles_content_blocks(tmp_path: Path) -> None:
    """Newer JSONL shape uses ``content`` as a list of blocks. Pull
    the first block's text."""
    p = tmp_path / "x.jsonl"
    _write_jsonl(
        p,
        [
            {
                "type": "user",
                "message": {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "from a block"},
                    ],
                },
            },
        ],
    )
    assert peek_first_user_message(p) == "from a block"


def test_peek_truncates_long_messages(tmp_path: Path) -> None:
    """120 chars is the rail-width budget; longer prompts get an
    ellipsis suffix."""
    long = "a" * 500
    p = tmp_path / "x.jsonl"
    _write_jsonl(p, [{"type": "user", "message": {"content": long}}])
    out = peek_first_user_message(p, max_chars=120)
    assert out is not None
    assert len(out) == 120
    assert out.endswith("…")


def test_peek_returns_none_for_no_user_turn(tmp_path: Path) -> None:
    """A JSONL with only summary/system records (no prompt yet) — the
    rail just shows no preview rather than the system message."""
    p = tmp_path / "x.jsonl"
    _write_jsonl(
        p,
        [
            {"type": "summary", "summary": "x"},
            {"type": "system", "content": "boot"},
        ],
    )
    assert peek_first_user_message(p) is None


def test_peek_returns_none_for_missing_file(tmp_path: Path) -> None:
    assert peek_first_user_message(tmp_path / "nope.jsonl") is None


def test_peek_skips_malformed_lines(tmp_path: Path) -> None:
    """A garbage line in the middle of the file shouldn't kill the
    scan — keep going until a real user turn surfaces."""
    p = tmp_path / "x.jsonl"
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(
        '{"type":"summary","summary":"x"}\n'
        "this is not json\n"
        '{"type":"user","message":{"content":"first real message"}}\n',
        encoding="utf-8",
    )
    assert peek_first_user_message(p) == "first real message"


def test_peek_collapses_whitespace(tmp_path: Path) -> None:
    """Multi-line / tab-rich prompts collapse to a single line for the
    rail — no need to render markdown in a one-liner badge."""
    p = tmp_path / "x.jsonl"
    _write_jsonl(
        p,
        [{"type": "user", "message": {"content": "line one\n\nline two\twith tab"}}],
    )
    out = peek_first_user_message(p)
    assert out == "line one line two with tab"


# ---------- Registry hook ----------


class _FakeSubs:
    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((method, params))


async def test_set_first_preview_emits_event() -> None:
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="t", kind=SessionKind.WRAPPED, cwd="/tmp")
    subs.broadcasts.clear()

    changed = await reg.set_first_preview(s.id, "explain ssm")
    assert changed is True
    events = [p for m, p in subs.broadcasts if m == "session_first_preview_resolved"]
    assert len(events) == 1
    assert events[0]["id"] == s.id
    assert events[0]["first_user_message"] == "explain ssm"


async def test_set_first_preview_idempotent() -> None:
    """Calling with the same text is a no-op so the watch_for_transcript
    hook can re-call without spamming events."""
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="t", kind=SessionKind.WRAPPED, cwd="/tmp")
    await reg.set_first_preview(s.id, "first")
    subs.broadcasts.clear()

    changed = await reg.set_first_preview(s.id, "first")
    assert changed is False
    assert subs.broadcasts == []


async def test_set_first_preview_persisted_to_session() -> None:
    """The cached value lands on Session.first_user_message so
    list_sessions surfaces it without an extra round-trip."""
    reg = Registry(hub_run_id="hr_t")
    s = await reg.register(name="t", kind=SessionKind.WRAPPED, cwd="/tmp")
    await reg.set_first_preview(s.id, "what is the ssm")
    assert (await reg.get(s.id)).first_user_message == "what is the ssm"
