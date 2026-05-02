"""record_chunk broadcasts the raw PTY bytes as a `pty_chunk` event so
TUI clients can pump them through their per-session vt emulator.
Without the broadcast, the chunk only lands on disk via LogWriter +
the in-memory FTS buffer — useful for transcripts but invisible to a
live PTY pane.
"""

from __future__ import annotations

import asyncio
import base64
from pathlib import Path
from typing import Any

from chubby.daemon.logs import LogWriter
from chubby.daemon.persistence import Database
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind


class _FakeSubs:
    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((method, params))


async def test_record_chunk_broadcasts_pty_chunk(tmp_path: Path) -> None:
    """A push_output (record_chunk) call should fan out a `pty_chunk`
    event with base64-encoded data + session_id. Subscribers without
    a writer attached to the session don't get the event (it's gated
    on having a writer)."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = await reg.register(
            name="t", kind=SessionKind.WRAPPED, cwd=str(tmp_path),
        )
        # record_chunk requires a writer be attached first.
        writer = LogWriter(tmp_path / "logs", color="#abc123", session_name="t")
        await reg.attach_log_writer(s.id, writer)

        chunk = b"hello \x1b[31mworld\x1b[0m\r\n"
        await reg.record_chunk(s.id, chunk, role="assistant")

        # First broadcast must be the pty_chunk for this session.
        pty_events = [
            (m, p) for (m, p) in subs.broadcasts if m == "pty_chunk"
        ]
        assert len(pty_events) == 1, (
            f"expected exactly one pty_chunk event, got {subs.broadcasts!r}"
        )
        _, params = pty_events[0]
        assert params["session_id"] == s.id
        assert params["role"] == "assistant"
        decoded = base64.b64decode(params["chunk_b64"])
        assert decoded == chunk, (
            f"chunk_b64 round-trip mismatch: got {decoded!r}, want {chunk!r}"
        )
    finally:
        await db.close()


async def test_record_chunk_no_writer_no_broadcast(tmp_path: Path) -> None:
    """If no writer is attached for a session, record_chunk no-ops
    early — and the early return must skip the broadcast too. This
    guards against silently fanning out chunks for sessions that
    aren't being logged (typically tmux-attached or in-flux sessions)."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = await reg.register(
            name="nowriter", kind=SessionKind.WRAPPED, cwd=str(tmp_path),
        )
        # Deliberately do NOT attach_log_writer.
        await reg.record_chunk(s.id, b"never seen", role="assistant")

        pty_events = [
            (m, p) for (m, p) in subs.broadcasts if m == "pty_chunk"
        ]
        assert pty_events == [], (
            f"expected no pty_chunk broadcast when writer is absent, got {pty_events!r}"
        )
    finally:
        await db.close()


async def test_pty_ring_replay_buffer(tmp_path: Path) -> None:
    """The registry's per-session pty ring is what get_pty_buffer
    returns — recent PTY bytes a TUI can replay to reconstruct
    claude's current screen on attach. The ring is bounded
    (64 KB by default) but holds far more than the FTS buffer,
    which gets cleared every 200 ms."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        s = await reg.register(
            name="ring", kind=SessionKind.WRAPPED, cwd=str(tmp_path),
        )
        await reg.attach_log_writer(
            s.id,
            LogWriter(tmp_path / "logs", color="#abc123", session_name="ring"),
        )

        # Three small chunks; ring should accumulate them all.
        await reg.record_chunk(s.id, b"line1\r\n", role="assistant")
        await reg.record_chunk(s.id, b"line2\r\n", role="assistant")
        await reg.record_chunk(s.id, b"line3\r\n", role="assistant")

        buf = reg.get_pty_buffer(s.id)
        assert buf == b"line1\r\nline2\r\nline3\r\n", (
            f"expected concatenated ring, got {buf!r}"
        )
    finally:
        await db.close()


async def test_pty_ring_caps_at_max(tmp_path: Path) -> None:
    """Writing past the cap trims to the most-recent N bytes — the
    tail must be intact (that's the bit the TUI vt emulator needs
    to reconstruct the visible screen)."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        # Lower the cap for a focused test.
        reg._pty_ring_cap = 16  # type: ignore[misc]
        s = await reg.register(
            name="big", kind=SessionKind.WRAPPED, cwd=str(tmp_path),
        )
        await reg.attach_log_writer(
            s.id,
            LogWriter(tmp_path / "logs", color="#abc123", session_name="big"),
        )

        await reg.record_chunk(s.id, b"AAAAAAAAAA", role="assistant")     # 10
        await reg.record_chunk(s.id, b"BBBBBBBBBB", role="assistant")     # 10  -> 20 total -> trim to last 16
        buf = reg.get_pty_buffer(s.id)
        assert len(buf) == 16, f"expected ring size 16, got {len(buf)}"
        # Tail must be intact: the last 16 bytes of "AAAAAAAAAA" + "BBBBBBBBBB".
        assert buf == (b"AAAAAAAAAA" + b"BBBBBBBBBB")[-16:], (
            f"ring tail wrong: {buf!r}"
        )
    finally:
        await db.close()


async def test_record_chunk_multiple_sessions_isolated(tmp_path: Path) -> None:
    """Two sessions with distinct writers should produce two distinct
    pty_chunk broadcasts addressed to the correct session_id. Cross-
    contamination here would leak one session's PTY into another's
    pane — the user-visible bug we're guarding against."""
    db = await Database.open(tmp_path / "s.db")
    try:
        subs = _FakeSubs()
        reg = Registry(hub_run_id="hr_t", db=db, subs=subs)  # type: ignore[arg-type]
        a = await reg.register(name="a", kind=SessionKind.WRAPPED, cwd=str(tmp_path))
        b = await reg.register(name="b", kind=SessionKind.WRAPPED, cwd=str(tmp_path))
        await reg.attach_log_writer(a.id, LogWriter(tmp_path / "la", color="#aaa", session_name="a"))
        await reg.attach_log_writer(b.id, LogWriter(tmp_path / "lb", color="#bbb", session_name="b"))

        await reg.record_chunk(a.id, b"alpha", role="assistant")
        await reg.record_chunk(b.id, b"beta", role="assistant")

        events = [
            (p["session_id"], base64.b64decode(p["chunk_b64"]))
            for (m, p) in subs.broadcasts if m == "pty_chunk"
        ]
        assert (a.id, b"alpha") in events
        assert (b.id, b"beta") in events
        assert len(events) == 2
    finally:
        await db.close()
