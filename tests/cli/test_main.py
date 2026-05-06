"""Tests for the chubby Typer CLI."""

from __future__ import annotations

from typer.testing import CliRunner

from chubby.cli.main import app

runner = CliRunner()


def test_help_lists_commands() -> None:
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    for cmd in ("up", "down", "ping", "list", "rename", "recolor", "send", "spawn", "tui"):
        assert cmd in result.stdout


def test_tui_help_works() -> None:
    result = runner.invoke(app, ["tui", "--help"])
    assert result.exit_code == 0
    # --force-download was retired when we dropped the auto-download
    # path; just verify the command's options are listed.
    assert "--focus" in result.stdout
    assert "--detached" in result.stdout
