"""Tests for the chub Typer CLI."""

from __future__ import annotations

from typer.testing import CliRunner

from chub.cli.main import app

runner = CliRunner()


def test_help_lists_commands() -> None:
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    for cmd in ("up", "down", "ping", "list", "rename", "recolor", "send", "spawn"):
        assert cmd in result.stdout
