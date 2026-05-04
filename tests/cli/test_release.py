"""Tests for ``chubby release`` (single + bulk filters).

Stubs ``Client`` so the typer command is exercised end-to-end without
a live daemon. Pattern lifted from ``tests/cli/test_preset_cli.py``.
"""

from __future__ import annotations

import time
from typing import Any

import pytest
from typer.testing import CliRunner

from chubby.proto.errors import ChubError, ErrorCode


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


def _session(
    *,
    sid: str,
    name: str,
    status: str = "idle",
    kind: str = "wrapped",
    tags: list[str] | None = None,
    last_activity_ms: int | None = None,
) -> dict[str, Any]:
    return {
        "id": sid,
        "name": name,
        "status": status,
        "kind": kind,
        "tags": tags or [],
        "last_activity_at": (
            last_activity_ms
            if last_activity_ms is not None
            else int(time.time() * 1000)
        ),
    }


def _make_fake_client(
    sessions: list[dict[str, Any]],
    captured: list[dict[str, Any]],
    *,
    refuse_release_for: set[str] | None = None,
):
    """Returns a fake Client that satisfies list_sessions / release_session
    / detach_session. ``refuse_release_for`` is a set of session ids
    that should reject release_session with INVALID_PAYLOAD (forcing
    the fallback to detach_session)."""
    refuse = refuse_release_for or set()

    class _FakeClient:
        def __init__(self, _sock: Any) -> None:
            pass

        async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured.append({"method": method, "params": params})
            if method == "list_sessions":
                return {"sessions": sessions}
            if method == "release_session":
                if params["id"] in refuse:
                    raise ChubError(
                        ErrorCode.INVALID_PAYLOAD,
                        "session has no bound claude session id yet",
                    )
                return {"claude_session_id": "c_" + params["id"], "cwd": "/tmp"}
            if method == "detach_session":
                return {}
            raise AssertionError(f"unexpected RPC: {method}")

        async def close(self) -> None:
            pass

    return _FakeClient


def test_release_single_name_dispatches_release_session(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    from chubby.cli.main import app
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []
    monkeypatch.setattr(
        release_cmd,
        "Client",
        _make_fake_client(
            [_session(sid="s1", name="web"), _session(sid="s2", name="api")],
            captured,
        ),
    )

    r = CliRunner().invoke(app, ["release", "web"])
    assert r.exit_code == 0, r.output
    rpc_methods = [c["method"] for c in captured]
    assert rpc_methods == ["list_sessions", "release_session"]
    assert captured[1]["params"] == {"id": "s1"}
    assert "released web" in r.output


def test_release_unknown_name_errors(monkeypatch: pytest.MonkeyPatch) -> None:
    from chubby.cli.main import app
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []
    monkeypatch.setattr(
        release_cmd,
        "Client",
        _make_fake_client([_session(sid="s1", name="web")], captured),
    )
    r = CliRunner().invoke(app, ["release", "ghost"])
    assert r.exit_code != 0
    assert "no live session" in (r.output + (r.stderr or ""))


def test_release_skips_dead_includes_readonly(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Dead sessions are skipped (they're already torn down) but
    readonly sessions are included so the user can clear the rail of
    auto-registered claude sessions they never spawned."""
    from chubby.cli.main import app
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []
    sessions = [
        _session(sid="s1", name="web"),
        _session(sid="s2", name="zombie", status="dead"),
        _session(sid="s3", name="watcher", kind="readonly"),
    ]
    monkeypatch.setattr(
        release_cmd, "Client", _make_fake_client(sessions, captured)
    )
    r = CliRunner().invoke(app, ["release", "--all", "--yes"])
    assert r.exit_code == 0, r.output
    released_ids = sorted(
        c["params"]["id"]
        for c in captured
        if c["method"] == "release_session"
    )
    # Wrapped + readonly both released; dead skipped.
    assert released_ids == ["s1", "s3"]


def test_release_tag_filters_by_tag(monkeypatch: pytest.MonkeyPatch) -> None:
    from chubby.cli.main import app
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []
    sessions = [
        _session(sid="s1", name="web", tags=["frontend"]),
        _session(sid="s2", name="api", tags=["backend"]),
        _session(sid="s3", name="ui-tests", tags=["frontend", "tests"]),
    ]
    monkeypatch.setattr(
        release_cmd, "Client", _make_fake_client(sessions, captured)
    )
    r = CliRunner().invoke(app, ["release", "--tag", "frontend", "--yes"])
    assert r.exit_code == 0, r.output
    released_ids = sorted(
        c["params"]["id"]
        for c in captured
        if c["method"] == "release_session"
    )
    assert released_ids == ["s1", "s3"]


def test_release_idle_since_drops_recent(monkeypatch: pytest.MonkeyPatch) -> None:
    from chubby.cli.main import app
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []
    now = int(time.time() * 1000)
    sessions = [
        _session(sid="s_old", name="stale", last_activity_ms=now - 3 * 60 * 60 * 1000),
        _session(sid="s_new", name="fresh", last_activity_ms=now - 30 * 1000),
    ]
    monkeypatch.setattr(
        release_cmd, "Client", _make_fake_client(sessions, captured)
    )
    r = CliRunner().invoke(app, ["release", "--idle-since", "1h", "--yes"])
    assert r.exit_code == 0, r.output
    released_ids = [
        c["params"]["id"]
        for c in captured
        if c["method"] == "release_session"
    ]
    assert released_ids == ["s_old"]


def test_release_falls_back_to_detach_when_no_claude_id(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """release_session refuses sessions without a bound claude id;
    the CLI falls back to detach_session for those."""
    from chubby.cli.main import app
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []
    monkeypatch.setattr(
        release_cmd,
        "Client",
        _make_fake_client(
            [_session(sid="s_unbound", name="early")],
            captured,
            refuse_release_for={"s_unbound"},
        ),
    )
    r = CliRunner().invoke(app, ["release", "early"])
    assert r.exit_code == 0, r.output
    methods = [c["method"] for c in captured]
    assert "release_session" in methods
    assert "detach_session" in methods
    assert "released early" in r.output


def test_release_rejects_names_plus_filters(monkeypatch: pytest.MonkeyPatch) -> None:
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["release", "web", "--all"])
    assert r.exit_code != 0
    assert "not both" in (r.output + (r.stderr or ""))


def test_release_no_args_errors(monkeypatch: pytest.MonkeyPatch) -> None:
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["release"])
    assert r.exit_code != 0
    out = r.output + (r.stderr or "")
    assert "--all" in out or "specify" in out


def test_release_idle_since_bad_format(monkeypatch: pytest.MonkeyPatch) -> None:
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["release", "--idle-since", "forever", "--yes"])
    assert r.exit_code != 0
    assert "30s" in (r.output + (r.stderr or ""))


def test_release_quiet_flag_emits_ids(monkeypatch: pytest.MonkeyPatch) -> None:
    from chubby.cli.main import app
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []
    sessions = [_session(sid="s1", name="web"), _session(sid="s2", name="api")]
    monkeypatch.setattr(
        release_cmd, "Client", _make_fake_client(sessions, captured)
    )
    # Global --quiet flag must come before subcommand under typer.
    r = CliRunner().invoke(app, ["--quiet", "release", "--all", "--yes"])
    assert r.exit_code == 0, r.output
    lines = [line for line in r.output.splitlines() if line]
    assert sorted(lines) == ["s1", "s2"]


def test_release_json_flag_emits_released_and_failed(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """JSON output is ``{"released": [...], "failed": [...]}`` so an
    outer agent sees BOTH outcomes. The original shape was just the
    released list, which silently swallowed daemon-side cleanup errors
    and showed ``[]`` even when work happened."""
    from chubby.cli.main import app
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []
    sessions = [_session(sid="s1", name="web")]
    monkeypatch.setattr(
        release_cmd, "Client", _make_fake_client(sessions, captured)
    )
    r = CliRunner().invoke(app, ["--json", "release", "--all", "--yes"])
    assert r.exit_code == 0, r.output
    import json as _json
    parsed = _json.loads(r.output.strip().splitlines()[-1])
    assert parsed == {
        "released": [{"id": "s1", "name": "web"}],
        "failed": [],
    }


def test_release_json_includes_failures(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """When release_session AND detach_session both error, the failure
    must show up in the JSON output rather than be silently dropped.
    """
    import chubby.cli.commands.release as release_cmd

    captured: list[dict[str, Any]] = []

    class _Failing:
        def __init__(self, _sock: Any) -> None:
            pass

        async def call(
            self, method: str, params: dict[str, Any]
        ) -> dict[str, Any]:
            captured.append({"method": method, "params": params})
            if method == "list_sessions":
                return {"sessions": [_session(sid="s1", name="web")]}
            if method in ("release_session", "detach_session"):
                raise ChubError(
                    ErrorCode.WRAPPER_UNREACHABLE, "wrapper gone"
                )
            raise AssertionError(f"unexpected RPC: {method}")

        async def close(self) -> None:
            pass

    monkeypatch.setattr(release_cmd, "Client", _Failing)

    from chubby.cli.main import app

    r = CliRunner().invoke(
        app, ["--json", "release", "--all", "--yes"]
    )
    assert r.exit_code == 0, r.output
    import json as _json

    parsed = _json.loads(r.output.strip().splitlines()[-1])
    assert parsed["released"] == []
    assert len(parsed["failed"]) == 1
    assert parsed["failed"][0]["name"] == "web"
    assert "wrapper gone" in parsed["failed"][0]["error"]
