"""Tests for the wrapper's defensive env-strip + injection.

When chubby is launched from inside another claude (a nested-agent
flow you sometimes want — Claude using chubby as an MCP-ish surface),
the parent env contains ``CLAUDE_CODE``, ``CLAUDECODE``,
``CLAUDE_CODE_ENTRYPOINT``, ``CLAUDE_SESSION_ID``. Passing those
through to the wrapped child confuses claude's session-id resolution
and the hooks we install. Strip before spawn.

Also: inject ``TERM_PROGRAM=chubby`` so tools can detect they're
running inside chubby, and ``FORCE_HYPERLINK=1`` so OSC 8 hyperlinks
render in chubby's bounded vt grid.
"""

from __future__ import annotations

import pytest

from chubby.wrapper.pty import _AGENT_ENV_VARS_TO_STRIP, _build_child_env


def test_strips_claude_code_markers(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("CLAUDE_CODE", "1")
    monkeypatch.setenv("CLAUDECODE", "1")
    monkeypatch.setenv("CLAUDE_CODE_ENTRYPOINT", "/path/to/claude")
    monkeypatch.setenv("CLAUDE_SESSION_ID", "abc-123")
    monkeypatch.setenv("CHUBBY_AGENT", "1")
    env = _build_child_env(extra=None)
    for var in _AGENT_ENV_VARS_TO_STRIP:
        assert var not in env, f"{var} should be stripped from child env"


def test_preserves_unrelated_vars(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("PATH", "/custom/path")
    monkeypatch.setenv("HOME", "/Users/test")
    env = _build_child_env(extra=None)
    assert env["PATH"] == "/custom/path"
    assert env["HOME"] == "/Users/test"


def test_injects_term_program(monkeypatch: pytest.MonkeyPatch) -> None:
    """Injected unconditionally — overwrites whatever the parent had."""
    monkeypatch.setenv("TERM_PROGRAM", "iTerm.app")
    env = _build_child_env(extra=None)
    assert env["TERM_PROGRAM"] == "chubby"


def test_force_hyperlink_default_set(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("FORCE_HYPERLINK", raising=False)
    env = _build_child_env(extra=None)
    assert env["FORCE_HYPERLINK"] == "1"


def test_force_hyperlink_user_override_preserved(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """If the user explicitly set FORCE_HYPERLINK (e.g. =0 to disable
    for a broken emulator), respect that."""
    monkeypatch.setenv("FORCE_HYPERLINK", "0")
    env = _build_child_env(extra=None)
    assert env["FORCE_HYPERLINK"] == "0"


def test_caller_extras_applied_last(monkeypatch: pytest.MonkeyPatch) -> None:
    """Caller-supplied env wins over both inherited and injected
    defaults — the wrapper passes ``CHUBBY_NAME``/``CHUBBY_HOME``
    via this path."""
    monkeypatch.setenv("CHUBBY_NAME", "from-parent")
    env = _build_child_env(extra={"CHUBBY_NAME": "from-caller", "MY_VAR": "x"})
    assert env["CHUBBY_NAME"] == "from-caller"
    assert env["MY_VAR"] == "x"
