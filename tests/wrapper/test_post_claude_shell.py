"""Unit tests for the post-claude-exit shell helpers in
chubby.wrapper.main: $SHELL detection and the inline "claude exited"
banner formatter. The full wrapper-spawns-shell loop is covered by
the existing wrapper PTY integration tests; these pin the pure
helpers so future edits don't silently change the user-facing copy
or the platform fallback order."""

from __future__ import annotations

import sys

import pytest

from chubby.wrapper.main import _post_claude_hint, _shell_path


def test_shell_path_honors_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """$SHELL wins when set — POSIX convention."""
    monkeypatch.setenv("SHELL", "/usr/local/bin/fish")
    assert _shell_path() == "/usr/local/bin/fish"


def test_shell_path_falls_back_to_zsh_on_macos(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """No $SHELL on macOS → /bin/zsh (Apple's default since Catalina)."""
    monkeypatch.delenv("SHELL", raising=False)
    monkeypatch.setattr(sys, "platform", "darwin")
    assert _shell_path() == "/bin/zsh"


def test_shell_path_falls_back_to_sh_when_nothing_else(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Last-resort fallback when nothing else is available."""
    monkeypatch.delenv("SHELL", raising=False)
    monkeypatch.setattr(sys, "platform", "linux")
    monkeypatch.setattr("os.path.exists", lambda p: False)
    assert _shell_path() == "/bin/sh"


def test_post_claude_hint_includes_resume_command() -> None:
    """The banner is what tells the user how to come back. Drop the
    --resume hint and the feature loses 80% of its value."""
    out = _post_claude_hint("abc-123-def").decode()
    assert "claude --resume abc-123-def" in out
    assert "claude exited" in out


def test_post_claude_hint_omits_resume_when_no_session_id() -> None:
    """When the JSONL never bound (e.g. claude crashed during init),
    we don't have an id to resume from. Show the bare exit notice
    instead of a malformed `claude --resume None`."""
    out = _post_claude_hint(None).decode()
    assert "claude exited" in out
    assert "--resume" not in out
    assert "None" not in out


def test_post_claude_hint_uses_dim_styling() -> None:
    """Banner reads as chubby chrome, not a real claude message —
    SGR 2 (dim) plus reset bracket the visible text."""
    out = _post_claude_hint("x").decode()
    assert "\x1b[2m" in out
    assert "\x1b[0m" in out


def test_post_claude_hint_brackets_with_em_dashes() -> None:
    """Visual marker — em-dash brackets so the user can spot the
    transition between claude output and shell prompt."""
    out = _post_claude_hint("x").decode()
    assert out.count("───") >= 2
