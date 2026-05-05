"""Tests for `chubby broadcast` filtering and dispatch.

The CLI's outbound calls go through ``chubby.cli.client.Client``; we patch that
class so we don't need a live daemon. This isolates the filter logic
(``--only`` / ``--tag``, dead/readonly skip) and the per-target inject loop.
"""

from __future__ import annotations

from typing import Any, ClassVar

import pytest
from typer.testing import CliRunner

import chubby.cli.commands.broadcast as broadcast_mod
from chubby.cli.main import app


class FakeClient:
    """Stand-in for ``chubby.cli.client.Client`` used by broadcast.run."""

    sessions: ClassVar[list[dict[str, Any]]] = []
    inject_calls: ClassVar[list[dict[str, Any]]] = []

    def __init__(self, *_args: Any, **_kwargs: Any) -> None:
        pass

    async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        if method == "list_sessions":
            return {"sessions": list(FakeClient.sessions)}
        if method == "inject":
            FakeClient.inject_calls.append(params)
            return {}
        raise AssertionError(f"unexpected method {method}")

    async def close(self) -> None:
        return None


@pytest.fixture
def fake_client(monkeypatch: pytest.MonkeyPatch) -> type[FakeClient]:
    FakeClient.sessions = []
    FakeClient.inject_calls = []
    monkeypatch.setattr(broadcast_mod, "Client", FakeClient)
    return FakeClient


def _session(
    name: str,
    *,
    sid: str | None = None,
    status: str = "idle",
    kind: str = "wrapped",
    tags: list[str] | None = None,
) -> dict[str, Any]:
    return {
        "id": sid or f"s_{name}",
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
        "tmux_target": None,
        "tags": tags or [],
        "ended_at": None,
    }


def test_broadcast_command_in_help() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "broadcast" in result.stdout


def test_broadcast_yes_sends_to_all_live_wrapped(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [_session("alpha"), _session("beta")]
    runner = CliRunner()
    result = runner.invoke(app, ["broadcast", "hello", "--yes"])
    assert result.exit_code == 0, result.stdout
    sent_ids = {c["session_id"] for c in fake_client.inject_calls}
    assert sent_ids == {"s_alpha", "s_beta"}
    assert "2 sent" in result.stdout


def test_broadcast_skips_dead_and_readonly(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [
        _session("alpha"),
        _session("zombie", status="dead"),
        _session("ro", kind="readonly"),
    ]
    runner = CliRunner()
    result = runner.invoke(app, ["broadcast", "hi", "--yes"])
    assert result.exit_code == 0, result.stdout
    sent_ids = [c["session_id"] for c in fake_client.inject_calls]
    assert sent_ids == ["s_alpha"]


def test_broadcast_only_filter(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [_session("alpha"), _session("beta"), _session("gamma")]
    runner = CliRunner()
    result = runner.invoke(app, ["broadcast", "hi", "--yes", "--only", "alpha", "--only", "gamma"])
    assert result.exit_code == 0, result.stdout
    sent_ids = sorted(c["session_id"] for c in fake_client.inject_calls)
    assert sent_ids == ["s_alpha", "s_gamma"]


def test_broadcast_tag_filter(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [
        _session("alpha", tags=["frontend"]),
        _session("beta", tags=["personal"]),
        _session("gamma", tags=["frontend", "backend"]),
    ]
    runner = CliRunner()
    result = runner.invoke(app, ["broadcast", "hi", "--yes", "--tag", "frontend"])
    assert result.exit_code == 0, result.stdout
    sent_ids = sorted(c["session_id"] for c in fake_client.inject_calls)
    assert sent_ids == ["s_alpha", "s_gamma"]


def test_broadcast_no_targets_says_so(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [_session("alpha", status="dead")]
    runner = CliRunner()
    result = runner.invoke(app, ["broadcast", "hi", "--yes"])
    assert result.exit_code == 0, result.stdout
    assert "no targets" in result.stdout
    assert fake_client.inject_calls == []


def test_broadcast_prompts_when_no_yes(fake_client: type[FakeClient]) -> None:
    fake_client.sessions = [_session("alpha")]
    runner = CliRunner()
    # Empty stdin -> typer.confirm aborts -> exit code != 0, no inject.
    result = runner.invoke(app, ["broadcast", "hi"], input="\n")
    assert result.exit_code != 0
    assert fake_client.inject_calls == []
