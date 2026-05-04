"""Tests for the global ``--json`` / ``--quiet`` flags + agent-context
auto-detection on the chubby CLI.

The contract:
- Pretty mode (default in an interactive shell) keeps the existing
  human-readable output.
- ``--json`` (or any of ``CLAUDE_CODE``, ``CLAUDECODE``, ``CODEX_CLI``,
  ``GEMINI_CLI``, ``CHUBBY_AGENT``, ``CI`` set) prints raw JSON arrays
  / objects with no envelope.
- ``--quiet`` prints one id per line for arrays and the bare id for
  single objects.
- Explicit flags win over env-var auto-detection.

We don't need a live daemon — every command we exercise is a thin
wrapper around ``Client.call``, which we monkey-patch to return a
canned response.
"""

from __future__ import annotations

import json
from typing import Any

import pytest
from typer.testing import CliRunner


# Two example sessions — what list_sessions would return.
_FAKE_SESSIONS = [
    {
        "id": "s_one",
        "name": "alpha",
        "color": "#abcdef",
        "status": "idle",
        "kind": "wrapped",
        "cwd": "/tmp/a",
    },
    {
        "id": "s_two",
        "name": "beta",
        "color": "#123456",
        "status": "thinking",
        "kind": "wrapped",
        "cwd": "/tmp/b",
    },
]


def _agent_env_vars() -> tuple[str, ...]:
    return (
        "CLAUDE_CODE",
        "CLAUDECODE",
        "CLAUDE_CODE_ENTRYPOINT",
        "CODEX_CLI",
        "GEMINI_CLI",
        "CHUBBY_AGENT",
        "CI",
    )


@pytest.fixture(autouse=True)
def _clear_agent_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """Tests must start in a clean env so auto-detection doesn't leak
    across tests (especially under ``CI=1`` in pytest's own env)."""
    for var in _agent_env_vars():
        monkeypatch.delenv(var, raising=False)


@pytest.fixture
def fake_client(monkeypatch: pytest.MonkeyPatch) -> None:
    """Replace ``chubby.cli.client.Client`` with a stub that returns
    ``_FAKE_SESSIONS`` for ``list_sessions`` and a synthetic spawn
    response for ``spawn_session``."""

    class _FakeClient:
        def __init__(self, sock: Any) -> None:
            pass

        async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            if method == "list_sessions":
                return {"sessions": _FAKE_SESSIONS}
            if method == "spawn_session":
                return {
                    "session": {
                        "id": "s_new",
                        "name": params.get("name", "x"),
                        "cwd": params.get("cwd", "/tmp"),
                    }
                }
            return {}

        async def close(self) -> None:
            pass

    # Each command imports Client at the top of its module; monkey-
    # patching the symbol on each module is the surgical option.
    import chubby.cli.commands.list as list_cmd
    import chubby.cli.commands.spawn as spawn_cmd

    monkeypatch.setattr(list_cmd, "Client", _FakeClient)
    monkeypatch.setattr(spawn_cmd, "Client", _FakeClient)


def test_list_pretty_default(fake_client: None) -> None:
    """No env vars, no flags → human-readable lines (one per session
    with the existing colored-prefix format)."""
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["list"])
    assert r.exit_code == 0, r.output
    # Pretty output contains the session names and statuses; should
    # NOT be valid JSON.
    assert "alpha" in r.output
    assert "beta" in r.output
    with pytest.raises(json.JSONDecodeError):
        json.loads(r.output.strip())


def test_list_json_explicit_flag(fake_client: None) -> None:
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["--json", "list"])
    assert r.exit_code == 0, r.output
    # Output is exactly the JSON array of sessions, no envelope.
    parsed = json.loads(r.output.strip())
    assert parsed == _FAKE_SESSIONS


def test_list_json_via_claude_code_env(
    fake_client: None, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Setting ``CLAUDE_CODE=1`` in the env (Claude Code subagent
    invoking us) flips the default to JSON without any CLI flag."""
    monkeypatch.setenv("CLAUDE_CODE", "1")
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["list"])
    assert r.exit_code == 0, r.output
    parsed = json.loads(r.output.strip())
    assert parsed == _FAKE_SESSIONS


@pytest.mark.parametrize(
    "var",
    [
        "CLAUDE_CODE",
        "CLAUDECODE",
        "CLAUDE_CODE_ENTRYPOINT",
        "CODEX_CLI",
        "GEMINI_CLI",
        "CHUBBY_AGENT",
        "CI",
    ],
)
def test_each_agent_env_var_triggers_json(
    fake_client: None, monkeypatch: pytest.MonkeyPatch, var: str
) -> None:
    """Each individual env var in the auto-detect set is sufficient."""
    monkeypatch.setenv(var, "1")
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["list"])
    assert r.exit_code == 0, r.output
    json.loads(r.output.strip())  # raises if not JSON


def test_list_quiet_prints_one_id_per_line(fake_client: None) -> None:
    """``--quiet`` prints just the ids — perfect for ``xargs``."""
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["--quiet", "list"])
    assert r.exit_code == 0, r.output
    lines = [ln for ln in r.output.strip().split("\n") if ln]
    assert lines == ["s_one", "s_two"]


def test_quiet_wins_over_json(fake_client: None) -> None:
    """If both ``--json`` and ``--quiet`` are passed, quiet's narrower
    intent wins (matches Superset's precedence)."""
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["--json", "--quiet", "list"])
    assert r.exit_code == 0, r.output
    lines = [ln for ln in r.output.strip().split("\n") if ln]
    assert lines == ["s_one", "s_two"]


def test_explicit_pretty_flag_beats_env(
    fake_client: None, monkeypatch: pytest.MonkeyPatch
) -> None:
    """No way to force pretty when CI is set today (we don't have a
    --pretty flag), so explicit flags only override toward more
    structure. Document by asserting CI alone gives JSON — the user
    must unset the env if they want pretty back."""
    monkeypatch.setenv("CI", "1")
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["list"])
    assert r.exit_code == 0, r.output
    json.loads(r.output.strip())


def test_spawn_quiet_prints_id(fake_client: None) -> None:
    """One-id-per-line for ``spawn`` enables pipelines like
    ``chubby spawn --name x --cwd /tmp --quiet | tee active-sessions``.
    """
    from chubby.cli.main import app

    r = CliRunner().invoke(
        app, ["--quiet", "spawn", "--name", "ws", "--cwd", "/tmp"]
    )
    assert r.exit_code == 0, r.output
    assert r.output.strip() == "s_new"


def test_spawn_json_prints_full_record(fake_client: None) -> None:
    from chubby.cli.main import app

    r = CliRunner().invoke(
        app, ["--json", "spawn", "--name", "ws", "--cwd", "/tmp"]
    )
    assert r.exit_code == 0, r.output
    parsed = json.loads(r.output.strip())
    assert parsed["session"]["id"] == "s_new"
    assert parsed["session"]["name"] == "ws"


def test_spawn_pretty_keeps_existing_message(fake_client: None) -> None:
    """Default human output preserves the historical ``spawned <id>
    (<name>)`` line so existing scripts that grep for it don't
    break."""
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["spawn", "--name", "ws", "--cwd", "/tmp"])
    assert r.exit_code == 0, r.output
    assert "spawned s_new" in r.output
    assert "(ws)" in r.output


def test_spawn_expands_tilde_in_cwd(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """``--cwd ~/foo`` must reach the daemon as an absolute path —
    typer doesn't expand ``~`` itself, and the user's shell may
    have left it literal (config file, MCP call, quoted arg).
    """
    captured: dict[str, Any] = {}

    class _FakeClient:
        def __init__(self, sock: Any) -> None:
            pass

        async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured["params"] = params
            return {"session": {"id": "s_new"}}

        async def close(self) -> None:
            pass

    import chubby.cli.commands.spawn as spawn_cmd
    monkeypatch.setattr(spawn_cmd, "Client", _FakeClient)
    # Pin HOME so the test is hermetic.
    monkeypatch.setenv("HOME", "/Users/test")

    from chubby.cli.main import app

    r = CliRunner().invoke(
        app, ["spawn", "--name", "ws", "--cwd", "~/myrepo"]
    )
    assert r.exit_code == 0, r.output
    assert captured["params"]["cwd"] == "/Users/test/myrepo"
