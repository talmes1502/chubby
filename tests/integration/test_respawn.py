"""Smoke test for `chub respawn`.

The plan calls for a smoke test that the CLI command is wired into the
Typer app — a richer integration test of the dead -> spawn -> rename
dance is intentionally deferred. See plan §"Task 9.3" and the v2 GC
note: rename-back currently collides with the SQLite UNIQUE
``(hub_run_id, name)`` index because the dead row still occupies the
original name in the same run; respawn works correctly inside the
*next* hub-run (where the dead row lives in a different run id) and
that path is exercised by ``chub up --resume`` in test_resume.py.
"""

from __future__ import annotations

from typer.testing import CliRunner

from chub.cli.main import app


def test_respawn_command_in_help() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    assert "respawn" in result.stdout


def test_respawn_help_lists_arguments() -> None:
    runner = CliRunner()
    result = runner.invoke(app, ["respawn", "--help"])
    assert result.exit_code == 0
    assert "name" in result.stdout.lower()
