"""Unit tests for ``chubby.daemon.run_processes`` — long-running
``run`` commands tied to session lifecycle."""

from __future__ import annotations

import asyncio
import os
from pathlib import Path

import pytest

from chubby.daemon.run_processes import RunProcessRegistry


@pytest.mark.asyncio
async def test_start_writes_pid_and_log_file(tmp_path: Path) -> None:
    reg = RunProcessRegistry()
    log_path = tmp_path / "logs" / "run-0.log"
    meta = await reg.start(
        session_id="s1",
        index=0,
        cmd="echo hello && sleep 5",
        cwd=tmp_path,
        env={},
        log_path=log_path,
        clock_ms=12345,
    )
    try:
        assert meta.pid > 0
        assert meta.log_path == log_path
        assert reg.is_running("s1", 0)
        # Wait for the echo to flush — Python launches the subprocess
        # but the kernel takes a beat to actually write.
        for _ in range(50):
            if log_path.exists() and log_path.stat().st_size > 0:
                break
            await asyncio.sleep(0.02)
        assert log_path.exists()
        assert b"hello" in log_path.read_bytes()
    finally:
        await reg.stop("s1", 0)


@pytest.mark.asyncio
async def test_start_rejects_duplicate(tmp_path: Path) -> None:
    reg = RunProcessRegistry()
    await reg.start(
        session_id="s1",
        index=0,
        cmd="sleep 5",
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "run-0.log",
        clock_ms=0,
    )
    try:
        with pytest.raises(RuntimeError, match="already running"):
            await reg.start(
                session_id="s1",
                index=0,
                cmd="sleep 5",
                cwd=tmp_path,
                env={},
                log_path=tmp_path / "run-0.log",
                clock_ms=0,
            )
    finally:
        await reg.stop("s1", 0)


@pytest.mark.asyncio
async def test_stop_kills_process(tmp_path: Path) -> None:
    reg = RunProcessRegistry()
    meta = await reg.start(
        session_id="s1",
        index=0,
        cmd="sleep 30",
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "run-0.log",
        clock_ms=0,
    )
    pid = meta.pid

    stopped = await reg.stop("s1", 0)
    assert stopped is True
    assert not reg.is_running("s1", 0)
    # PID should no longer be alive — give the kernel a moment.
    for _ in range(50):
        try:
            os.kill(pid, 0)
            await asyncio.sleep(0.05)
        except ProcessLookupError:
            return
    pytest.fail(f"pid {pid} still alive after stop")


@pytest.mark.asyncio
async def test_stop_unknown_returns_false(tmp_path: Path) -> None:
    reg = RunProcessRegistry()
    stopped = await reg.stop("nope", 0)
    assert stopped is False


@pytest.mark.asyncio
async def test_stop_all_for_session(tmp_path: Path) -> None:
    reg = RunProcessRegistry()
    await reg.start(
        session_id="s1",
        index=0,
        cmd="sleep 30",
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "0.log",
        clock_ms=0,
    )
    await reg.start(
        session_id="s1",
        index=1,
        cmd="sleep 30",
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "1.log",
        clock_ms=0,
    )
    # A different session — must not be touched by stop_all_for_session("s1").
    await reg.start(
        session_id="other",
        index=0,
        cmd="sleep 30",
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "other.log",
        clock_ms=0,
    )

    stopped = await reg.stop_all_for_session("s1")
    assert stopped == 2
    assert not reg.is_running("s1", 0)
    assert not reg.is_running("s1", 1)
    assert reg.is_running("other", 0)
    await reg.stop_all_for_session("other")


@pytest.mark.asyncio
async def test_natural_exit_clears_registry_entry(tmp_path: Path) -> None:
    """A ``run`` command that exits on its own (e.g. typo in
    ``bun dev``) must drop out of the registry so the next ``:run 0``
    can re-launch it without hitting the duplicate guard."""
    reg = RunProcessRegistry()
    await reg.start(
        session_id="s1",
        index=0,
        cmd="true",  # exits immediately
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "0.log",
        clock_ms=0,
    )
    # Wait for the watcher to notice the exit.
    for _ in range(100):
        if not reg.is_running("s1", 0):
            return
        await asyncio.sleep(0.02)
    pytest.fail("registry didn't drop the entry after natural exit")


@pytest.mark.asyncio
async def test_list_for_session_returns_running_only(tmp_path: Path) -> None:
    reg = RunProcessRegistry()
    await reg.start(
        session_id="s1",
        index=0,
        cmd="sleep 30",
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "0.log",
        clock_ms=111,
    )
    await reg.start(
        session_id="s2",
        index=0,
        cmd="sleep 30",
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "s2.log",
        clock_ms=222,
    )
    procs = reg.list_for_session("s1")
    assert len(procs) == 1
    assert procs[0].session_id == "s1"
    assert procs[0].started_at_ms == 111
    await reg.stop_all_for_session("s1")
    await reg.stop_all_for_session("s2")
    assert reg.list_for_session("s1") == []


@pytest.mark.asyncio
async def test_terminate_falls_back_to_sigkill(tmp_path: Path) -> None:
    """If SIGHUP isn't enough (process traps it), we SIGKILL after the
    grace window. ``trap '' HUP`` is the cheap way to simulate a script
    that ignores SIGHUP."""
    reg = RunProcessRegistry()
    await reg.start(
        session_id="s1",
        index=0,
        cmd="trap '' HUP; sleep 30",
        cwd=tmp_path,
        env={},
        log_path=tmp_path / "0.log",
        clock_ms=0,
    )
    stopped = await reg.stop("s1", 0)
    assert stopped is True
    assert not reg.is_running("s1", 0)
