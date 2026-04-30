"""Tests for the get_session_history RPC.

The TUI uses this to seed its viewport on first sight of a session, so the
conversation persists across `chubby down` + `chubby up` cycles. The daemon reads
the bound JSONL on disk; only ``user`` / ``assistant`` records become turns,
non-conversation records (e.g. ``last-prompt``, ``attachment``) are dropped.
"""

from __future__ import annotations

import json
from pathlib import Path

from chubby.daemon.handlers import CallContext
from chubby.daemon.main import _build_registry
from chubby.daemon.persistence import Database
from chubby.daemon.registry import Registry
from chubby.daemon.runs import HubRun
from chubby.daemon.session import SessionKind
from chubby.daemon.subscriptions import SubscriptionHub


async def _noop_write(_b: bytes) -> None:  # pragma: no cover - unused
    return None


def _ctx() -> CallContext:
    """A minimal CallContext for handler tests; on_close is a no-op."""
    return CallContext(connection_id=0, write=_noop_write, on_close=lambda _cb: None)


async def test_get_session_history_reads_jsonl_turns(
    tmp_path: Path, monkeypatch
) -> None:
    """Records of type user / assistant become turns; everything else is
    dropped. Tool-use blocks render as the compact `⏺ Tool` summary the
    rest of the codebase already uses (see _extract_turn_text)."""
    # Seed a fake ~/.claude/projects/<encoded>/<id>.jsonl.
    fake_root = tmp_path / "projects"
    sub = fake_root / "encoded-cwd"
    sub.mkdir(parents=True)
    claude_session_id = "abc-123"
    jsonl = sub / f"{claude_session_id}.jsonl"
    jsonl.write_text(
        # last-prompt-style record — must be skipped.
        json.dumps({"type": "last-prompt", "value": "ignored"}) + "\n"
        # User turn with plain string content.
        + json.dumps(
            {
                "type": "user",
                "timestamp": "2025-01-01T00:00:00.000Z",
                "message": {"role": "user", "content": "hello"},
            }
        ) + "\n"
        # Assistant turn with text + tool_use blocks.
        + json.dumps(
            {
                "type": "assistant",
                "timestamp": "2025-01-01T00:00:01.500Z",
                "message": {
                    "role": "assistant",
                    "content": [
                        {"type": "text", "text": "let me look"},
                        {"type": "tool_use", "name": "Read",
                         "input": {"file_path": "/tmp/x.py"}},
                    ],
                },
            }
        ) + "\n"
        # An attachment-style record — must be skipped.
        + json.dumps({"type": "attachment", "data": "..."}) + "\n"
    )

    # Point the projects-root resolver at our fake tree.
    import chubby.daemon.hooks as hooks_mod
    monkeypatch.setattr(hooks_mod, "claude_projects_root", lambda: fake_root)

    # Build the dependency graph the handler closure expects.
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = SubscriptionHub()
        run_dir = tmp_path / "run"
        run_dir.mkdir(exist_ok=True)
        from chubby.daemon.events import EventLog
        run = HubRun(
            id="hr_t",
            started_at=0,
            dir=run_dir,
            event_log=EventLog(run_dir / "events.ndjson"),
        )
        reg = Registry(hub_run_id=run.id, db=db, subs=subs)
        s = await reg.register(name="x", kind=SessionKind.WRAPPED, cwd=str(tmp_path))
        # Bind the chubby session to the Claude session id whose JSONL we wrote.
        await reg.set_claude_session_id(s.id, claude_session_id)

        handlers = _build_registry(reg, run, db, subs)
        out = await handlers.invoke(
            "get_session_history",
            {"session_id": s.id, "limit": 500},
            _ctx(),
        )
    finally:
        await db.close()

    assert out is not None
    turns = out["turns"]
    assert len(turns) == 2

    # First turn: user "hello".
    assert turns[0]["role"] == "user"
    assert turns[0]["text"] == "hello"
    # Timestamp parses to a positive ms value.
    assert turns[0]["ts"] > 0

    # Second turn: assistant prose lives in `text`; the tool_use block
    # is split out into the structured `tool_calls` array so the TUI can
    # render it as a styled box rather than inline ⏺-prefixed text.
    assert turns[1]["role"] == "assistant"
    assert "let me look" in turns[1]["text"]
    # Tool name/args MUST NOT leak into the prose body.
    assert "⏺ Read" not in turns[1]["text"]
    assert "/tmp/x.py" not in turns[1]["text"]
    tcs = turns[1].get("tool_calls", [])
    assert len(tcs) == 1
    assert tcs[0]["name"] == "Read"
    # Read's canonical key is file_path, so the summary echoes it.
    assert tcs[0]["summary"] == "/tmp/x.py"


async def test_get_session_history_returns_empty_when_unbound(
    tmp_path: Path, monkeypatch
) -> None:
    """If the chubby session has no claude_session_id yet (JSONL not bound),
    the handler returns an empty list rather than failing."""
    fake_root = tmp_path / "projects"
    fake_root.mkdir()
    import chubby.daemon.hooks as hooks_mod
    monkeypatch.setattr(hooks_mod, "claude_projects_root", lambda: fake_root)

    db = await Database.open(tmp_path / "s.db")
    try:
        subs = SubscriptionHub()
        run_dir = tmp_path / "run"
        run_dir.mkdir(exist_ok=True)
        from chubby.daemon.events import EventLog
        run = HubRun(
            id="hr_t",
            started_at=0,
            dir=run_dir,
            event_log=EventLog(run_dir / "events.ndjson"),
        )
        reg = Registry(hub_run_id=run.id, db=db, subs=subs)
        s = await reg.register(name="y", kind=SessionKind.WRAPPED, cwd=str(tmp_path))
        # Note: no set_claude_session_id call.

        handlers = _build_registry(reg, run, db, subs)
        out = await handlers.invoke(
            "get_session_history",
            {"session_id": s.id, "limit": 500},
            _ctx(),
        )
    finally:
        await db.close()

    assert out == {"turns": []}
