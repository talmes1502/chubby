"""Regression test for the "ghost text in claude's input box" bug.

Symptom (from user screenshots): after the wrapper auto-respawns
claude (because claude exited and the wrapper relaunched it under the
same chubby session id), the TUI's vt grid kept stale content from
the prior claude. The new claude's partial clears (per-line ESC[2K
plus cursor moves) didn't touch every cell, so old text — banner,
last user prompt — showed through where the new claude rendered an
empty input box.

Fix: the wrapper now pushes a synthetic ESC[2J ESC[3J ESC[H
(erase-display + erase-scrollback + home-cursor) chunk *before* the
new claude starts producing output. The daemon broadcasts it to TUI
subscribers like any other PTY chunk; chubby's vt processes it and
clears its grid. This test confirms that chunk fires as the first
push_chunk on every claude launch (first iteration *and* respawn).
"""

from __future__ import annotations

import asyncio
import signal
import sys

import pytest

from chubby.proto.rpc import Event

_ERASE_ALL = b"\x1b[2J\x1b[3J\x1b[H"


# Test scaffolding copied from test_restart_loop.py (the tests/ package
# isn't importable as a module, so we re-roll the small fakes inline).


class _FakePty:
    def __init__(self, argv: list[str], *, cwd: str, env: dict | None = None) -> None:
        self.argv = argv
        self.cwd = cwd
        self.closed = asyncio.Event()
        self.write_user_calls: list[bytes] = []
        self._eof = asyncio.Event()
        self.signal_child_called = False
        self.terminate_called = False

    async def start(self) -> None:
        return None

    @property
    def pid(self) -> int:
        return 4242

    async def iter_output(self):
        yield b"hello\n"
        await self._eof.wait()
        self.closed.set()
        return

    async def write_user(self, payload: bytes) -> None:
        self.write_user_calls.append(payload)

    async def resize(self, rows: int, cols: int) -> None:
        return None

    async def terminate(self) -> None:
        self.terminate_called = True
        self.closed.set()

    def signal_child(self, sig: int = signal.SIGTERM) -> None:
        self.signal_child_called = True
        self._eof.set()


class _FakeClient:
    def __init__(self) -> None:
        self.session_id: str | None = None
        self.register_calls: list[dict] = []
        self.update_pid_calls: list[int] = []
        self.push_chunks: list[bytes] = []
        self._events: asyncio.Queue[Event] = asyncio.Queue()

    async def register(self, *, name: str, cwd: str, pid: int, tags: list[str], claude_pid: int | None = None) -> str:
        self.register_calls.append({
            "name": name, "cwd": cwd, "pid": pid, "tags": tags, "claude_pid": claude_pid,
        })
        self.session_id = "s_test"
        return "s_test"

    async def update_claude_pid(self, *, claude_pid: int) -> None:
        self.update_pid_calls.append(claude_pid)

    async def push_chunk(self, *, seq: int, data: bytes, role: str = "raw") -> None:
        self.push_chunks.append(data)

    async def events(self) -> asyncio.Queue:
        return self._events

    async def session_ended(self) -> None:
        return None

    async def close(self) -> None:
        return None


class _NullBuffer:
    def read1(self, n: int = -1) -> bytes:
        return b""

    def read(self, n: int = -1) -> bytes:
        return b""


class _NullStdin:
    buffer = _NullBuffer()


async def _run_one_iteration(
    monkeypatch: pytest.MonkeyPatch,
    *,
    is_first_iteration: bool,
    resume: str | None,
) -> _FakeClient:
    """Drive _run_one_claude through one iteration with the test
    scaffolding, then return the _FakeClient so callers can assert on
    push_chunks."""
    import chubby.wrapper.main as wm

    monkeypatch.setattr(wm, "PtySession", _FakePty)

    async def fake_wait_sid(pid: int, timeout_s: float) -> str | None:
        return "abcdef01-0000-0000-0000-000000000000"

    monkeypatch.setattr(wm, "_wait_for_claude_session_id", fake_wait_sid)
    monkeypatch.setattr(wm.signal, "signal", lambda *_a, **_k: None)
    monkeypatch.setattr(sys, "stdin", _NullStdin(), raising=False)

    client = _FakeClient()
    if not is_first_iteration:
        client.session_id = "s_test"  # pretend we registered earlier

    async def kick_restart() -> None:
        await asyncio.sleep(0.05)
        await client._events.put(
            Event(method="restart_claude", params={"session_id": "s_test"})
        )

    asyncio.create_task(kick_restart())

    await asyncio.wait_for(
        wm._run_one_claude(
            client=client,  # type: ignore[arg-type]
            cwd="/tmp",
            passthrough=[],
            resume=resume,
            is_first_iteration=is_first_iteration,
            name="ref",
            tags=[],
        ),
        timeout=5.0,
    )
    return client


async def test_first_claude_launch_pushes_erase_display(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """First iteration: harmless on a fresh grid but still pushed
    so the contract is uniform across iterations."""
    client = await _run_one_iteration(
        monkeypatch, is_first_iteration=True, resume=None,
    )
    assert client.push_chunks, "wrapper never pushed any chunks"
    assert client.push_chunks[0] == _ERASE_ALL, (
        f"first chunk should be erase-display; got {client.push_chunks[0]!r}"
    )


async def test_respawn_pushes_erase_display_before_new_render(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Respawn iteration (is_first_iteration=False, resume=<sid>): the
    erase-display chunk must fire BEFORE any output the new claude
    produces, otherwise the TUI's vt grid keeps stale cells from the
    prior claude (the bug's symptom)."""
    client = await _run_one_iteration(
        monkeypatch, is_first_iteration=False,
        resume="00112233-4455-6677-8899-aabbccddeeff",
    )
    assert client.push_chunks, "wrapper never pushed any chunks"
    assert client.push_chunks[0] == _ERASE_ALL, (
        f"first chunk on respawn should be erase-display; got "
        f"{client.push_chunks[0]!r}"
    )
