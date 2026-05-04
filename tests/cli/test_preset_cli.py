"""Tests for ``chubby preset`` CLI commands.

Covers the create/list/delete/show round-trip and the ``apply``
flow's dispatch to ``spawn_session``. We stub Client.call so the
tests don't need a live daemon.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from typer.testing import CliRunner


@pytest.fixture(autouse=True)
def _isolated_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("CHUBBY_HOME", str(tmp_path))
    return tmp_path


@pytest.fixture(autouse=True)
def _clear_agent_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """The CLI's --json auto-detect would otherwise corrupt our
    pretty-output assertions when running under CI."""
    for var in (
        "CLAUDE_CODE",
        "CLAUDECODE",
        "CLAUDE_CODE_ENTRYPOINT",
        "CODEX_CLI",
        "GEMINI_CLI",
        "CHUBBY_AGENT",
        "CI",
    ):
        monkeypatch.delenv(var, raising=False)


def test_preset_create_list_delete_roundtrip(_isolated_home: Path) -> None:
    from chubby.cli.main import app

    runner = CliRunner()
    # Create.
    r = runner.invoke(
        app,
        ["preset", "create", "web", "--cwd", "~/repo/web", "--tags", "frontend"],
    )
    assert r.exit_code == 0, r.output
    assert "saved preset web" in r.output

    # List.
    r = runner.invoke(app, ["preset", "list"])
    assert r.exit_code == 0, r.output
    assert "web" in r.output
    assert "cwd=~/repo/web" in r.output

    # Show.
    r = runner.invoke(app, ["preset", "show", "web"])
    assert r.exit_code == 0, r.output
    assert "preset: web" in r.output

    # Delete.
    r = runner.invoke(app, ["preset", "delete", "web"])
    assert r.exit_code == 0, r.output
    assert "deleted preset web" in r.output

    # Empty list after delete.
    r = runner.invoke(app, ["preset", "list"])
    assert r.exit_code == 0, r.output
    assert "(no presets)" in r.output


def test_preset_delete_unknown_returns_error(_isolated_home: Path) -> None:
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["preset", "delete", "ghost"])
    assert r.exit_code == 1
    # Errors go to stderr; CliRunner mixes them by default.
    assert "no preset named 'ghost'" in r.output or "no preset named 'ghost'" in r.stderr


def test_preset_apply_dispatches_to_spawn_session(
    _isolated_home: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """``preset apply`` should resolve templates and call
    ``spawn_session`` with the rendered params."""
    from chubby.cli.main import app

    captured: list[dict[str, Any]] = []

    class _FakeClient:
        def __init__(self, sock: Any) -> None:
            pass

        async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured.append({"method": method, "params": params})
            return {"session": {"id": "s_new", "name": params.get("name", "?")}}

        async def close(self) -> None:
            pass

    import chubby.cli.commands.preset as preset_cmd

    monkeypatch.setattr(preset_cmd, "Client", _FakeClient)

    runner = CliRunner()
    runner.invoke(
        app,
        ["preset", "create", "web", "--cwd", "/tmp/web", "--branch", "wip-{date}"],
    )
    r = runner.invoke(app, ["preset", "apply", "web"])
    assert r.exit_code == 0, r.output
    assert len(captured) == 1
    call = captured[0]
    assert call["method"] == "spawn_session"
    assert call["params"]["name"] == "web"
    assert call["params"]["cwd"] == "/tmp/web"
    # ``wip-{date}`` was rendered (date substituted).
    assert call["params"]["branch"].startswith("wip-")
    assert "{" not in call["params"]["branch"]


def test_preset_apply_overrides_name(_isolated_home: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """``preset apply web --name custom`` overrides the resolved
    name without otherwise affecting the preset."""
    from chubby.cli.main import app

    captured: list[dict[str, Any]] = []

    class _FakeClient:
        def __init__(self, sock: Any) -> None:
            pass

        async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured.append(params)
            return {"session": {"id": "s_new"}}

        async def close(self) -> None:
            pass

    import chubby.cli.commands.preset as preset_cmd

    monkeypatch.setattr(preset_cmd, "Client", _FakeClient)

    runner = CliRunner()
    runner.invoke(app, ["preset", "create", "web", "--cwd", "/tmp/web"])
    r = runner.invoke(app, ["preset", "apply", "web", "--name", "feature-x"])
    assert r.exit_code == 0, r.output
    assert captured[0]["name"] == "feature-x"
    assert captured[0]["cwd"] == "/tmp/web"


def test_preset_list_json_output(_isolated_home: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """The shared OUT formatter applies — ``--json`` returns the
    raw array."""
    monkeypatch.setenv("CLAUDE_CODE", "1")  # auto-JSON via env
    from chubby.cli.main import app

    runner = CliRunner()
    runner.invoke(app, ["preset", "create", "web", "--cwd", "/x"])
    r = runner.invoke(app, ["preset", "list"])
    assert r.exit_code == 0, r.output
    parsed = json.loads(r.output.strip())
    assert isinstance(parsed, list)
    assert parsed[0]["name"] == "web"
