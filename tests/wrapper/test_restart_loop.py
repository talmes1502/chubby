"""Tests for the wrapper's per-claude restart loop. Verifies that
``_run_one_claude`` builds the correct argv with ``--resume`` on
restart, and that an inbound ``restart_claude`` event triggers the
restart path.
"""

from __future__ import annotations

import asyncio
import signal
import sys
from pathlib import Path

from chubby.proto.rpc import Event
from chubby.wrapper.client import WrapperClient


class FakePty:
    """Stand-in for PtySession for the restart-loop test. Records the
    argv it was constructed with, fires EOF when ``signal_child`` is
    called (mimicking claude exiting under SIGTERM), and exposes a few
    sync hooks so the test can drive the lifecycle deterministically.
    """

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
        # Yield a single startup banner so the trust-watcher has something
        # to chew on; then block until eof is set.
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


class FakeClient:
    """Stand-in for WrapperClient used by `_run_one_claude`. Tracks
    register / update_claude_pid / push_chunk / events and exposes an
    inbound-event queue we can publish into to simulate daemon pushes.
    """

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


async def test_run_one_claude_restart_path(monkeypatch) -> None:
    """First iteration: registers, claude runs. Then we publish a
    ``restart_claude`` event; ``_run_one_claude`` returns with
    ``restart_requested=True`` and a captured session id. We don't
    actually run a second iteration here — that's covered by the
    wrapper's main loop, which is exercised separately. We just confirm
    the building blocks behave."""
    import chubby.wrapper.main as wm

    monkeypatch.setattr(wm, "PtySession", FakePty)
    # Skip the per-pid sessionId capture (no real claude on disk).
    async def fake_wait_sid(pid: int, timeout_s: float) -> str | None:
        return "abcdef01-0000-0000-0000-000000000000"

    monkeypatch.setattr(wm, "_wait_for_claude_session_id", fake_wait_sid)
    # Don't attach a real signal handler.
    monkeypatch.setattr(wm.signal, "signal", lambda *_a, **_k: None)
    # Replace sys.stdin entirely; pytest's DontReadFromInput has a
    # read-only ``buffer`` property so we can't patch a single attr.
    monkeypatch.setattr(sys, "stdin", _NullStdin(), raising=False)

    client = FakeClient()

    async def kick_restart() -> None:
        await asyncio.sleep(0.05)
        await client._events.put(
            Event(method="restart_claude", params={"session_id": "s_test"})
        )

    asyncio.create_task(kick_restart())

    res = await asyncio.wait_for(
        wm._run_one_claude(
            client=client,  # type: ignore[arg-type]
            cwd="/tmp",
            passthrough=[],
            resume=None,
            is_first_iteration=True,
            name="ref",
            tags=[],
        ),
        timeout=5.0,
    )

    assert res.restart_requested is True
    assert res.session_id == "abcdef01-0000-0000-0000-000000000000"
    assert client.register_calls and client.register_calls[0]["name"] == "ref"
    # update_claude_pid is NOT called on the first iteration — that's
    # the second-iteration fingerprint.
    assert client.update_pid_calls == []


async def test_run_one_claude_resume_argv(monkeypatch) -> None:
    """Second iteration: ``resume=<sid>`` flows into the claude argv as
    ``--resume <sid>`` and ``update_claude_pid`` is called instead of
    ``register``."""
    import chubby.wrapper.main as wm

    monkeypatch.setattr(wm, "PtySession", FakePty)

    async def fake_wait_sid(pid: int, timeout_s: float) -> str | None:
        return None  # not interesting for this test

    monkeypatch.setattr(wm, "_wait_for_claude_session_id", fake_wait_sid)
    monkeypatch.setattr(wm.signal, "signal", lambda *_a, **_k: None)
    monkeypatch.setattr(sys, "stdin", _NullStdin(), raising=False)

    client = FakeClient()
    client.session_id = "s_test"  # already registered

    captured_argv: list[list[str]] = []

    real_FakePty = FakePty

    class CapturingPty(real_FakePty):
        def __init__(self, argv, **kw):
            captured_argv.append(list(argv))
            super().__init__(argv, **kw)

    monkeypatch.setattr(wm, "PtySession", CapturingPty)

    # End the iteration quickly: schedule a restart so the iter exits.
    async def kick_eof() -> None:
        await asyncio.sleep(0.05)
        await client._events.put(
            Event(method="restart_claude", params={"session_id": "s_test"})
        )

    asyncio.create_task(kick_eof())

    res = await asyncio.wait_for(
        wm._run_one_claude(
            client=client,  # type: ignore[arg-type]
            cwd="/tmp",
            passthrough=["--print"],
            resume="00112233-4455-6677-8899-aabbccddeeff",
            is_first_iteration=False,
            name="ref",
            tags=[],
        ),
        timeout=5.0,
    )

    assert res.restart_requested is True
    assert captured_argv, "PtySession was never constructed"
    argv = captured_argv[0]
    # Must start with claude, then --resume <sid>, then passthrough
    assert argv[0] == "claude"
    assert "--resume" in argv
    i = argv.index("--resume")
    assert argv[i + 1] == "00112233-4455-6677-8899-aabbccddeeff"
    assert "--print" in argv
    # update_claude_pid called (not register)
    assert client.update_pid_calls == [4242]
    assert client.register_calls == []


class _NullBuffer:
    """Stand-in for ``sys.stdin.buffer`` whose read1 returns empty bytes
    immediately so the stdin pump exits without blocking the loop."""

    def read1(self, n: int = -1) -> bytes:
        return b""

    def read(self, n: int = -1) -> bytes:
        return b""


class _NullStdin:
    """Stand-in for ``sys.stdin`` exposing only the .buffer attribute the
    wrapper's pump uses (read1/read returning empty bytes)."""

    buffer = _NullBuffer()
