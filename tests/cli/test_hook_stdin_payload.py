"""Tests for the Stop / SessionStart hook commands' stdin-JSON path.

Claude Code passes hook event data to user commands as a JSON object
on stdin (``{"session_id": "...", "cwd": "...", ...}``). Pre-fix the
commands required a ``--claude-session-id`` flag whose value came from
``$CLAUDE_SESSION_ID``, an env var Claude Code does not export — the
flag evaluated empty, typer rejected the call, every Stop hook fired
into the void, and chubby's AWAITING_USER redraw never triggered.

These tests cover:
  - ``read_hook_payload`` parses well-formed JSON, returns ``{}`` for
    missing/empty/malformed input
  - ``mark-idle`` reads the session_id from stdin when no flag is
    given, and silently no-ops when neither source produces an id
  - ``register-readonly`` reads session_id + cwd from stdin
"""

from __future__ import annotations

import io
import json
from typing import Any

import pytest
from typer.testing import CliRunner

from chubby.cli.commands._hook_input import read_hook_payload


def test_read_hook_payload_parses_json(monkeypatch: pytest.MonkeyPatch) -> None:
    payload = {"session_id": "abc", "cwd": "/tmp"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    monkeypatch.setattr("sys.stdin.isatty", lambda: False, raising=False)

    # The isatty patch above doesn't bind to StringIO; patch via a
    # tiny wrapper instead.
    class _NonTtyStdin(io.StringIO):
        def isatty(self) -> bool:
            return False

    monkeypatch.setattr("sys.stdin", _NonTtyStdin(json.dumps(payload)))
    assert read_hook_payload() == payload


def test_read_hook_payload_returns_empty_on_empty_stdin(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _NonTtyStdin(io.StringIO):
        def isatty(self) -> bool:
            return False

    monkeypatch.setattr("sys.stdin", _NonTtyStdin(""))
    assert read_hook_payload() == {}


def test_read_hook_payload_returns_empty_on_garbage(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _NonTtyStdin(io.StringIO):
        def isatty(self) -> bool:
            return False

    monkeypatch.setattr("sys.stdin", _NonTtyStdin("not json at all"))
    assert read_hook_payload() == {}


def test_read_hook_payload_returns_empty_on_tty(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Don't hang waiting for stdin when invoked interactively."""

    class _TtyStdin(io.StringIO):
        def isatty(self) -> bool:
            return True

    monkeypatch.setattr("sys.stdin", _TtyStdin("ignored"))
    assert read_hook_payload() == {}


def test_mark_idle_reads_session_id_from_stdin(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The Stop hook flow: claude pipes ``{"session_id": ...}`` to
    stdin, no CLI flags. mark-idle must extract the id and call the
    daemon with it."""
    captured: dict[str, Any] = {}

    class _FakeClient:
        def __init__(self, sock: Any) -> None:
            pass

        async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured["method"] = method
            captured["params"] = params
            return {}

        async def close(self) -> None:
            pass

    monkeypatch.setattr("chubby.cli.commands.mark_idle.Client", _FakeClient)

    from chubby.cli.main import app

    # CliRunner.invoke(input=...) feeds the bytes to the command's
    # stdin (a non-tty pipe), which is exactly the shape Claude Code
    # uses when firing a hook.
    r = CliRunner().invoke(
        app,
        ["mark-idle"],
        input=json.dumps({"session_id": "claude-uuid-from-stdin"}),
    )
    assert r.exit_code == 0, r.output
    assert captured == {
        "method": "mark_idle",
        "params": {"claude_session_id": "claude-uuid-from-stdin"},
    }


def test_mark_idle_no_payload_is_silent_noop(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """When neither --claude-session-id nor stdin provides an id, the
    command silently exits 0 — a broken hook config must never block
    Claude."""
    called = False

    class _FakeClient:
        def __init__(self, sock: Any) -> None:
            nonlocal called
            called = True

        async def call(self, *_a: Any, **_k: Any) -> dict[str, Any]:
            return {}

        async def close(self) -> None:
            pass

    monkeypatch.setattr("chubby.cli.commands.mark_idle.Client", _FakeClient)

    class _NonTtyStdin(io.StringIO):
        def isatty(self) -> bool:
            return False

    monkeypatch.setattr("sys.stdin", _NonTtyStdin(""))
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["mark-idle"])
    assert r.exit_code == 0, r.output
    assert called is False, "no session id available → must not even open a daemon connection"


def test_register_readonly_reads_payload_from_stdin(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """SessionStart hook: claude pipes ``{"session_id": ..., "cwd":
    ...}``. register-readonly must use both."""
    captured: dict[str, Any] = {}

    class _FakeClient:
        def __init__(self, sock: Any) -> None:
            pass

        async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured["method"] = method
            captured["params"] = params
            return {}

        async def close(self) -> None:
            pass

    monkeypatch.setattr("chubby.cli.commands.register_readonly.Client", _FakeClient)

    from chubby.cli.main import app

    r = CliRunner().invoke(
        app,
        ["register-readonly"],
        input=json.dumps({"session_id": "abc", "cwd": "/Users/foo/project"}),
    )
    assert r.exit_code == 0, r.output
    assert captured["method"] == "register_readonly"
    assert captured["params"]["claude_session_id"] == "abc"
    assert captured["params"]["cwd"] == "/Users/foo/project"
