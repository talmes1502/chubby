"""Tests for ``chub attach`` CLI and the daemon's scan_candidates / attach_tmux RPCs."""

from __future__ import annotations

from typing import Any, ClassVar

import pytest
from typer.testing import CliRunner

import chub.cli.commands.attach as attach_mod
from chub.cli.main import app


class FakeClient:
    """Stand-in for ``chub.cli.client.Client`` used by attach.run."""

    candidates: ClassVar[list[dict[str, Any]]] = []
    attach_calls: ClassVar[list[dict[str, Any]]] = []

    def __init__(self, *_args: Any, **_kwargs: Any) -> None:
        pass

    async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        if method == "scan_candidates":
            return {"candidates": list(FakeClient.candidates)}
        if method == "attach_tmux":
            FakeClient.attach_calls.append(params)
            return {
                "session": {
                    "id": "s_x",
                    "hub_run_id": "hr_x",
                    "name": params["name"],
                    "color": "#abcdef",
                    "kind": "tmux_attached",
                    "cwd": params["cwd"],
                    "created_at": 0,
                    "last_activity_at": 0,
                    "status": "idle",
                    "pid": params["pid"],
                    "claude_session_id": None,
                    "tmux_target": params["tmux_target"],
                    "tags": params["tags"],
                    "ended_at": None,
                }
            }
        raise AssertionError(f"unexpected method {method}")

    async def close(self) -> None:
        return None


@pytest.fixture
def fake_client(monkeypatch: pytest.MonkeyPatch) -> type[FakeClient]:
    FakeClient.candidates = []
    FakeClient.attach_calls = []
    monkeypatch.setattr(attach_mod, "Client", FakeClient)
    return FakeClient


def _candidate(
    pid: int,
    *,
    cwd: str = "/tmp/x",
    tmux_target: str | None = "ws:0.0",
    classification: str = "tmux_full",
    already_attached: bool = False,
) -> dict[str, Any]:
    return {
        "pid": pid,
        "cwd": cwd,
        "tmux_target": tmux_target,
        "classification": classification,
        "already_attached": already_attached,
    }


def test_attach_command_in_help() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "attach" in result.stdout


def test_attach_help_shows_options() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "--help"])
    assert result.exit_code == 0
    assert "--pick" in result.stdout
    assert "--list" in result.stdout
    assert "--all" in result.stdout


def test_attach_list_outputs_json(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = [_candidate(123)]
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "--list"])
    assert result.exit_code == 0, result.stdout
    assert '"pid"' in result.stdout
    assert "123" in result.stdout
    assert fake_client.attach_calls == []


def test_attach_list_filters_already_attached(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = [
        _candidate(1, already_attached=True),
        _candidate(2),
    ]
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "--list"])
    assert result.exit_code == 0, result.stdout
    assert '"pid": 2' in result.stdout
    assert '"pid": 1' not in result.stdout


def test_attach_all_attaches_all_tmux(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = [
        _candidate(11, cwd="/work/a", tmux_target="ws:0.0"),
        _candidate(12, cwd="/work/b", tmux_target="ws:1.0"),
        _candidate(13, cwd="/work/c", tmux_target=None, classification="promote_required"),
    ]
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "--all"])
    assert result.exit_code == 0, result.stdout
    pids = sorted(c["pid"] for c in fake_client.attach_calls)
    assert pids == [11, 12]


def test_attach_all_no_candidates(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = [
        _candidate(1, classification="promote_required", tmux_target=None),
    ]
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "--all"])
    assert result.exit_code == 0, result.stdout
    assert "no tmux-attachable" in result.stdout
    assert fake_client.attach_calls == []


def test_attach_specific_target(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = [_candidate(99, cwd="/p/q", tmux_target="my:0.0")]
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "tmux:my:0.0"])
    assert result.exit_code == 0, result.stdout
    assert len(fake_client.attach_calls) == 1
    call = fake_client.attach_calls[0]
    assert call["tmux_target"] == "my:0.0"
    assert call["pid"] == 99


def test_attach_specific_with_name(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = [_candidate(99, cwd="/p/q", tmux_target="my:0.0")]
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "tmux:my:0.0", "--name", "custom"])
    assert result.exit_code == 0, result.stdout
    assert fake_client.attach_calls[0]["name"] == "custom"


def test_attach_specific_target_not_found(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = [_candidate(99, tmux_target="other:0.0")]
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "tmux:missing:0.0"])
    assert result.exit_code != 0
    assert fake_client.attach_calls == []


def test_attach_no_args_is_error(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = []
    runner = CliRunner()
    result = runner.invoke(app, ["attach"])
    assert result.exit_code != 0


def test_attach_pick_interactive(fake_client: type[FakeClient]) -> None:
    fake_client.candidates = [
        _candidate(11, cwd="/work/a", tmux_target="ws:0.0"),
        _candidate(12, cwd="/work/b", tmux_target="ws:1.0"),
    ]
    runner = CliRunner()
    result = runner.invoke(app, ["attach", "--pick"], input="2\n")
    assert result.exit_code == 0, result.stdout
    assert len(fake_client.attach_calls) == 1
    assert fake_client.attach_calls[0]["pid"] == 12


# --- daemon-side: scan_candidates over a live daemon -------------------------


async def test_scan_candidates_returns_list_against_live_daemon(
    chub_home: Any, tmp_path: Any
) -> None:
    """Smoke test: scan_candidates RPC returns a list (possibly empty)."""
    import asyncio

    from chub.cli.client import Client
    from chub.daemon import main as chubd_main

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
        r = await client.call("scan_candidates", {})
        assert "candidates" in r
        assert isinstance(r["candidates"], list)
        for c in r["candidates"]:
            assert "pid" in c
            assert "classification" in c
            assert "already_attached" in c
        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
