"""Tests for the force-claude-redraw flow that fixes ghost text in
the input box after each turn.

Symptom (from user screenshots): the cells where the user typed their
prompt before submission stayed visible in chubby's bounded vt grid
because claude's diff-render uses cursor-forward to skip cells it
thinks already have correct content. Real terminals don't show this
because old content scrolls into scrollback; chubby's grid keeps it.

Fix: every time the daemon flips a session's status to AWAITING_USER
(i.e., claude finished a turn), Registry._force_claude_redraw fires
two things in order:
  1. Push a synthetic erase-display chunk (\x1b[2J\x1b[3J\x1b[H) via
     record_chunk so chubby's vt grid clears for every TUI subscriber.
  2. Send a redraw_claude event to the wrapped claude's wrapper. The
     wrapper handles it by SIGWINCHing claude, which triggers its
     standard "resize → redraw from scratch" path — every cell is
     rewritten.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from chubby.daemon.logs import LogWriter
from chubby.daemon.persistence import Database
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind, SessionStatus


_ERASE_ALL = b"\x1b[2J\x1b[3J\x1b[H"


class _FakeSubs:
    """Captures pty_chunk + status broadcasts so tests can assert on
    order/content."""

    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((method, params))


async def test_awaiting_user_pushes_erase_display_then_redraw_event(
    tmp_path: Path,
) -> None:
    """The exact failure mode the user reported: after a turn-end the
    input box shows the previously-typed prompt as ghost text. Verify
    the redraw flow fires in the correct order on AWAITING_USER."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = await reg.register(name="t", kind=SessionKind.WRAPPED, cwd=str(tmp_path))

        # Attach a writer so record_chunk doesn't no-op (and so the
        # broadcast actually fires).
        writer = LogWriter(tmp_path / "logs", color="#abc123", session_name="t")
        await reg.attach_log_writer(s.id, writer)

        # Attach a fake wrapper writer so the redraw_claude event has
        # somewhere to land. Capture the encoded events for assertion.
        wrapper_events: list[bytes] = []

        async def fake_wrapper_write(data: bytes) -> None:
            wrapper_events.append(data)

        await reg.attach_wrapper(s.id, fake_wrapper_write)

        subs.broadcasts.clear()  # ignore session_added
        await reg.update_status(s.id, SessionStatus.AWAITING_USER)

        # Step 1 of the redraw: erase-display broadcast as a pty_chunk.
        pty_chunks = [p for m, p in subs.broadcasts if m == "pty_chunk"]
        assert pty_chunks, "no pty_chunk broadcast — erase-display didn't fire"
        import base64
        first_chunk = base64.b64decode(pty_chunks[0]["chunk_b64"])
        assert first_chunk == _ERASE_ALL, (
            f"first pty_chunk after AWAITING_USER should be erase-display; "
            f"got {first_chunk!r}"
        )

        # Step 2: redraw_claude event delivered to the wrapper.
        redraw_seen = any(b'"method":"redraw_claude"' in ev for ev in wrapper_events)
        assert redraw_seen, (
            f"wrapper never received redraw_claude event; got {wrapper_events!r}"
        )
    finally:
        await db.close()


async def test_idle_status_does_not_force_redraw(tmp_path: Path) -> None:
    """Only AWAITING_USER triggers the redraw; an IDLE flip (e.g.,
    inject after thinking) shouldn't waste a SIGWINCH or flash the vt
    grid."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = await reg.register(name="t", kind=SessionKind.WRAPPED, cwd=str(tmp_path))
        writer = LogWriter(tmp_path / "logs", color="#abc123", session_name="t")
        await reg.attach_log_writer(s.id, writer)

        wrapper_events: list[bytes] = []

        async def fake_wrapper_write(data: bytes) -> None:
            wrapper_events.append(data)

        await reg.attach_wrapper(s.id, fake_wrapper_write)

        subs.broadcasts.clear()
        await reg.update_status(s.id, SessionStatus.IDLE)

        # No erase-display chunks, no redraw event.
        pty_chunks = [p for m, p in subs.broadcasts if m == "pty_chunk"]
        assert pty_chunks == [], (
            f"IDLE flip should NOT push pty_chunks; got {pty_chunks!r}"
        )
        redraw_seen = any(b'"method":"redraw_claude"' in ev for ev in wrapper_events)
        assert not redraw_seen, "IDLE flip should NOT fire redraw_claude"
    finally:
        await db.close()


async def test_redraw_event_uses_correct_session_id(tmp_path: Path) -> None:
    """The redraw_claude event must carry the right session_id so the
    wrapper signals the correct claude (in case multiple wrappers
    share one daemon connection somehow — defensive)."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = await reg.register(name="t", kind=SessionKind.WRAPPED, cwd=str(tmp_path))
        writer = LogWriter(tmp_path / "logs", color="#abc123", session_name="t")
        await reg.attach_log_writer(s.id, writer)

        wrapper_events: list[bytes] = []

        async def fake_wrapper_write(data: bytes) -> None:
            wrapper_events.append(data)

        await reg.attach_wrapper(s.id, fake_wrapper_write)

        await reg.update_status(s.id, SessionStatus.AWAITING_USER)

        # Find the redraw event and parse its session_id.
        redraw = next(
            (ev for ev in wrapper_events if b'"method":"redraw_claude"' in ev),
            None,
        )
        assert redraw is not None, "no redraw_claude event sent"
        # Events are JSON-RPC framed; strip the framing and parse.
        # The encoder writes a length-prefixed JSON object; locate
        # the JSON braces to extract.
        text = redraw.decode("utf-8")
        start = text.index("{")
        end = text.rindex("}") + 1
        rec = json.loads(text[start:end])
        assert rec.get("method") == "redraw_claude"
        assert rec.get("params", {}).get("session_id") == s.id
    finally:
        await db.close()
