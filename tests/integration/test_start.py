"""Integration test for ``chubby start`` — one-command bootstrap."""

from __future__ import annotations

import asyncio
import os
import signal
import time
from pathlib import Path

from typer.testing import CliRunner

from chubby.cli.main import app
from chubby.daemon import paths


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def test_chub_start_bootstraps_daemon(chub_home: Path) -> None:
    """``chubby start --no-tui --no-auto-attach --no-hooks`` brings up a daemon
    even if none was running before.
    """
    # Make sure no daemon was running in this isolated CHUBBY_HOME.
    assert not paths.pid_path().exists()

    runner = CliRunner()
    result = runner.invoke(
        app,
        ["start", "--no-tui", "--no-auto-attach", "--no-hooks"],
    )
    try:
        assert result.exit_code == 0, result.stdout
        assert "daemon" in result.stdout.lower()
        # Daemon's pidfile should now exist and point to a live process.
        assert paths.pid_path().exists()
        pid = int(paths.pid_path().read_text().strip())
        assert _pid_alive(pid)
        assert paths.sock_path().exists()
    finally:
        # Clean up the daemon we spawned.
        if paths.pid_path().exists():
            try:
                pid = int(paths.pid_path().read_text().strip())
                os.kill(pid, signal.SIGTERM)
                # Wait briefly for it to exit.
                deadline = time.monotonic() + 3.0
                while time.monotonic() < deadline and _pid_alive(pid):
                    time.sleep(0.05)
            except (ProcessLookupError, ValueError, FileNotFoundError):
                pass


def test_chub_start_in_help() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "start" in result.stdout


def test_chub_start_help_lists_flags() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["start", "--help"])
    assert result.exit_code == 0, result.stdout
    assert "--auto-attach" in result.stdout
    assert "--no-hooks" in result.stdout
    assert "--no-tui" in result.stdout


def test_chub_start_skips_when_daemon_already_running(chub_home: Path) -> None:
    """If a daemon is already up, chubby start re-uses it (no respawn)."""
    from chubby.daemon import main as chubbyd_main

    async def driver() -> tuple[int, str]:
        stop = asyncio.Event()
        server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
        # Wait for the socket to come up.
        for _ in range(100):
            if paths.sock_path().exists() and paths.pid_path().exists():
                break
            await asyncio.sleep(0.02)
        assert paths.sock_path().exists()
        original_pid = int(paths.pid_path().read_text().strip())
        # Run chubby start in a thread (CliRunner is sync). Use a separate
        # event-loop-friendly subprocess approach here: we just use asyncio's
        # to_thread to run the Typer CLI synchronously.
        runner = CliRunner()
        result = await asyncio.to_thread(
            runner.invoke,
            app,
            ["start", "--no-tui", "--no-auto-attach", "--no-hooks"],
        )
        out = result.stdout
        assert result.exit_code == 0, out
        new_pid = int(paths.pid_path().read_text().strip())
        # Daemon should not have been respawned.
        assert new_pid == original_pid
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
        return original_pid, out

    asyncio.run(driver())
