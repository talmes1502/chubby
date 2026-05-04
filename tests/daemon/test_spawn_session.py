"""Tests for the ``spawn_session`` RPC handler — specifically the
empty-cwd → $HOME fallback added when we made cwd optional.

These tests patch ``asyncio.create_subprocess_exec`` so we never actually
launch a wrapper, then read back the ``--cwd`` argument the daemon
chose.
"""

from __future__ import annotations

import asyncio
import json
import shutil
import tempfile
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest

from chubby.daemon import main as chubbyd_main
from chubby.proto import frame
from chubby.proto.errors import ErrorCode


@pytest.fixture
def short_home() -> Iterator[Path]:
    """macOS AF_UNIX sun_path is limited to ~104 bytes; pytest's tmp_path
    is too long when used as CHUBBY_HOME. Use a short /tmp dir."""
    d = Path(tempfile.mkdtemp(prefix="chubby-"))
    try:
        yield d
    finally:
        shutil.rmtree(d, ignore_errors=True)


class _FakeProc:
    """Stand-in for an ``asyncio.subprocess.Process`` — the daemon
    spawns this for both the wrapper subprocess and the git
    ``repo_root`` probe added by Phase 2 (project_config). For the
    git probe we return empty stdout so ``repo_root`` correctly
    falls through to ``None`` (no git repo detected) and the spawn
    flow uses the cwd as-is. For the wrapper itself the fake is
    just kept alive long enough for the daemon to drop its
    reference."""

    def __init__(self) -> None:
        self.returncode: int | None = 0
        self.pid: int = 99999

    async def wait(self) -> int:
        return 0

    async def communicate(self) -> tuple[bytes, bytes]:
        return b"", b""


async def _rpc(sock_path: Path, method: str, params: dict) -> dict:
    reader, writer = await asyncio.open_unix_connection(str(sock_path))
    body = json.dumps(
        {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
    ).encode()
    writer.write(frame.encode(body))
    await writer.drain()
    raw = await frame.read_frame(reader)
    writer.close()
    await writer.wait_closed()
    assert raw is not None
    return json.loads(raw)


async def _start_daemon(short_home: Path, monkeypatch) -> tuple[Path, asyncio.Event, asyncio.Task]:
    monkeypatch.setenv("CHUBBY_HOME", str(short_home))
    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = short_home / "hub.sock"
    for _ in range(50):
        if sock.exists():
            break
        await asyncio.sleep(0.02)
    assert sock.exists(), "daemon failed to bind socket"
    return sock, stop, server_task


def _capture_subprocess(monkeypatch: pytest.MonkeyPatch) -> list[tuple[Any, ...]]:
    """Replace ``asyncio.create_subprocess_exec`` with a recorder and
    return the list of captured positional arg tuples. Patches both the
    asyncio module and the daemon module's local binding (the daemon
    imports it via ``import asyncio`` so patching the module is enough)."""
    captured: list[tuple[Any, ...]] = []

    async def fake_create(*args: Any, **kwargs: Any) -> _FakeProc:
        captured.append(args)
        return _FakeProc()

    monkeypatch.setattr(asyncio, "create_subprocess_exec", fake_create)
    return captured


def _cwd_arg_from_call(call_args: tuple[Any, ...]) -> str:
    """Pull the value passed to ``--cwd`` out of a captured argv tuple."""
    parts = list(call_args)
    for i, a in enumerate(parts):
        if a == "--cwd" and i + 1 < len(parts):
            return str(parts[i + 1])
    raise AssertionError(f"--cwd not found in argv: {parts!r}")


def _wrapper_call(captured: list[tuple[Any, ...]]) -> tuple[Any, ...]:
    """Return the captured subprocess invocation for the wrapper.
    Phase 2 added a ``git rev-parse`` probe before the wrapper spawn,
    so ``captured[0]`` is no longer guaranteed to be the wrapper —
    walk the list and pick the call that contains ``--cwd``."""
    for call in captured:
        if "--cwd" in call:
            return call
    raise AssertionError(f"no wrapper call (with --cwd) in {captured!r}")


async def test_spawn_session_empty_cwd_rejected(
    short_home: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """``spawn_session`` with ``cwd=""`` is rejected loudly so sloppy
    scripted invocations surface instead of silently defaulting. The
    TUI modal pre-fills cwd and the CLI defaults to ``os.getcwd()``,
    so this should never fire from a real client."""
    captured = _capture_subprocess(monkeypatch)

    sock, stop, server_task = await _start_daemon(short_home, monkeypatch)
    try:
        out = await _rpc(sock, "spawn_session", {"name": "no_cwd", "cwd": ""})
        assert "error" in out, f"expected error, got {out!r}"
        assert out["error"]["code"] == ErrorCode.INVALID_PAYLOAD.value
        assert "cwd is required" in out["error"]["message"]
        # We must NOT have launched a subprocess.
        assert not captured, "subprocess should not be launched on rejection"
    finally:
        stop.set()
        await server_task


async def test_spawn_session_omitted_cwd_rejected(
    short_home: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """The schema now requires ``cwd``; an omitted field surfaces a
    pydantic validation error (mapped to INVALID_PAYLOAD)."""
    captured = _capture_subprocess(monkeypatch)

    sock, stop, server_task = await _start_daemon(short_home, monkeypatch)
    try:
        out = await _rpc(sock, "spawn_session", {"name": "no_cwd_field"})
        assert "error" in out, f"expected error, got {out!r}"
        assert out["error"]["code"] == ErrorCode.INVALID_PAYLOAD.value
        assert not captured, "subprocess should not be launched on rejection"
    finally:
        stop.set()
        await server_task


async def test_spawn_session_explicit_cwd_passes_through(
    short_home: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Sanity check the happy path: an explicit cwd is forwarded as-is."""
    captured = _capture_subprocess(monkeypatch)

    sock, stop, server_task = await _start_daemon(short_home, monkeypatch)
    try:
        out = await _rpc(
            sock, "spawn_session", {"name": "with_cwd", "cwd": "/var/tmp"}
        )
        assert "result" in out or out["error"]["code"] == ErrorCode.INTERNAL.value
        assert captured, "expected create_subprocess_exec to be called"
        assert _cwd_arg_from_call(_wrapper_call(captured)) == "/var/tmp"
    finally:
        stop.set()
        await server_task
