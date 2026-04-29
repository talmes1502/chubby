"""Tests for ``chub promote`` / ``chub detach`` and the daemon-side handlers."""

from __future__ import annotations

import asyncio
from typing import Any, ClassVar

import pytest
from typer.testing import CliRunner

import chub.cli.commands.detach as detach_mod
import chub.cli.commands.promote as promote_mod
from chub.cli.main import app
from chub.daemon.attach import promote as promote_daemon
from chub.daemon.attach import tmux as tmux_mod
from chub.daemon.registry import Registry
from chub.daemon.session import SessionKind, SessionStatus

# --- CLI smoke tests ---------------------------------------------------------


def test_promote_in_help() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "promote" in result.stdout


def test_detach_in_help() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "detach" in result.stdout


def test_promote_help_works() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["promote", "--help"])
    assert result.exit_code == 0
    assert "NAME" in result.stdout.upper() or "name" in result.stdout


def test_detach_help_works() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["detach", "--help"])
    assert result.exit_code == 0
    assert "NAME" in result.stdout.upper() or "name" in result.stdout


# --- CLI behaviour against fake clients -------------------------------------


class FakeClient:
    sessions: ClassVar[list[dict[str, Any]]] = []
    calls: ClassVar[list[tuple[str, dict[str, Any]]]] = []

    def __init__(self, *_args: Any, **_kwargs: Any) -> None:
        pass

    async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        FakeClient.calls.append((method, params))
        if method == "list_sessions":
            return {"sessions": list(FakeClient.sessions)}
        return {}

    async def close(self) -> None:
        return None


def _session(
    name: str,
    *,
    sid: str = "s_x",
    kind: str = "tmux_attached",
    status: str = "idle",
) -> dict[str, Any]:
    return {
        "id": sid,
        "hub_run_id": "hr_test",
        "name": name,
        "color": "#abcdef",
        "kind": kind,
        "cwd": "/tmp",
        "created_at": 0,
        "last_activity_at": 0,
        "status": status,
        "pid": 1,
        "claude_session_id": None,
        "tmux_target": "ws:0.0",
        "tags": [],
        "ended_at": None,
    }


@pytest.fixture
def fake_client(monkeypatch: pytest.MonkeyPatch) -> type[FakeClient]:
    FakeClient.sessions = []
    FakeClient.calls = []
    monkeypatch.setattr(promote_mod, "Client", FakeClient)
    monkeypatch.setattr(detach_mod, "Client", FakeClient)
    return FakeClient


def test_detach_calls_daemon(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [_session("alpha", sid="s_alpha")]
    runner = CliRunner()
    result = runner.invoke(app, ["detach", "alpha"])
    assert result.exit_code == 0, result.stdout
    methods = [c[0] for c in fake_client.calls]
    assert "detach_session" in methods
    detach_call = next(c for c in fake_client.calls if c[0] == "detach_session")
    assert detach_call[1] == {"id": "s_alpha"}


def test_detach_unknown_name_errors(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = []
    runner = CliRunner()
    result = runner.invoke(app, ["detach", "ghost"])
    assert result.exit_code != 0


def test_promote_only_readonly(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [_session("alpha", kind="wrapped")]
    runner = CliRunner()
    result = runner.invoke(app, ["promote", "alpha"])
    assert result.exit_code != 0


def test_promote_calls_daemon(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [_session("readonly1", sid="s_ro", kind="readonly")]
    runner = CliRunner()
    result = runner.invoke(app, ["promote", "readonly1"])
    assert result.exit_code == 0, result.stdout
    methods = [c[0] for c in fake_client.calls]
    assert "promote_session" in methods


# --- promote module unit tests ----------------------------------------------


async def test_wait_for_exit_returns_true_for_dead_pid() -> None:
    # Pid 1 always exists, but a very large pid is unlikely to.
    dead = 999_999_999
    ok = await promote_daemon.wait_for_exit(dead, timeout=1.0)
    assert ok is True


async def test_wait_for_exit_times_out_for_live_pid() -> None:
    import os

    ok = await promote_daemon.wait_for_exit(os.getpid(), timeout=1.0)
    assert ok is False


# --- daemon-side detach handler against a Registry --------------------------


async def test_detach_session_marks_dead_and_stops_watcher(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """detach_session sets the stop event and updates status to DEAD."""

    # Avoid invoking the real tmux binary by stubbing the watcher.
    async def fake_watch_pane(
        registry: Registry, session_id: str, target: str, *, stop: asyncio.Event
    ) -> None:
        await stop.wait()

    monkeypatch.setattr(tmux_mod, "watch_pane", fake_watch_pane)

    reg = Registry(hub_run_id="hr_t")
    s = await reg.register(
        name="t",
        kind=SessionKind.TMUX_ATTACHED,
        cwd="/tmp",
        pid=42,
        tmux_target="ws:0.0",
    )
    stop = asyncio.Event()
    reg._tmux_stops[s.id] = stop
    task = asyncio.create_task(tmux_mod.watch_pane(reg, s.id, "ws:0.0", stop=stop))

    # Simulate detach_session handler logic.
    await reg.get(s.id)
    reg._tmux_stops[s.id].set()
    await reg.update_status(s.id, SessionStatus.DEAD)

    await asyncio.wait_for(task, timeout=2.0)
    s2 = await reg.get(s.id)
    assert s2.status is SessionStatus.DEAD


async def test_promote_session_rejects_unknown_id(
    chub_home: Any,
) -> None:
    """promote_session via the daemon raises for an unknown session id."""
    from chub.cli.client import Client
    from chub.daemon import main as chubd_main
    from chub.proto.errors import ChubError

    stop = asyncio.Event()
    server_task = asyncio.create_task(chubd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists()
    try:
        client = Client(sock)
        with pytest.raises(ChubError):
            await client.call("promote_session", {"id": "no-such-session"})
        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
