"""Tests for ``chub install-hooks`` (idempotent merge into ~/.claude/settings.json)."""

from __future__ import annotations

import importlib
import json
from pathlib import Path

import pytest
from typer.testing import CliRunner


def _reload_module(monkeypatch: pytest.MonkeyPatch, fake_home: Path) -> None:
    """Reload modules that captured ``Path.home()`` at import time."""
    monkeypatch.setenv("HOME", str(fake_home))
    from chub.cli import main as cli_main
    from chub.cli.commands import install_hooks

    importlib.reload(install_hooks)
    importlib.reload(cli_main)


def test_install_hooks_idempotent(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    _reload_module(monkeypatch, fake_home)

    from chub.cli.main import app

    runner = CliRunner()
    r1 = runner.invoke(app, ["install-hooks"])
    assert r1.exit_code == 0, r1.output
    r2 = runner.invoke(app, ["install-hooks"])
    assert r2.exit_code == 0, r2.output

    settings_path = fake_home / ".claude" / "settings.json"
    settings = json.loads(settings_path.read_text())

    session_start = settings["hooks"]["SessionStart"]
    stop = settings["hooks"]["Stop"]
    assert [h["name"] for h in session_start].count("chub-register-readonly") == 1
    assert [h["name"] for h in stop].count("chub-mark-idle") == 1


def test_install_hooks_preserves_existing(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    claude_dir = fake_home / ".claude"
    claude_dir.mkdir()
    pre_existing = {
        "hooks": {
            "SessionStart": [
                {"name": "user-custom", "command": "echo custom"}
            ]
        },
        "theme": "dark",
    }
    (claude_dir / "settings.json").write_text(json.dumps(pre_existing))

    _reload_module(monkeypatch, fake_home)
    from chub.cli.main import app

    runner = CliRunner()
    r = runner.invoke(app, ["install-hooks"])
    assert r.exit_code == 0, r.output

    settings = json.loads((claude_dir / "settings.json").read_text())
    names = [h["name"] for h in settings["hooks"]["SessionStart"]]
    assert "user-custom" in names
    assert "chub-register-readonly" in names
    assert settings["theme"] == "dark"
    # A backup of the pre-existing settings.json was created.
    assert (claude_dir / "settings.json.bak").exists()


def test_install_hooks_dry_run_does_not_write(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    _reload_module(monkeypatch, fake_home)
    from chub.cli.main import app

    runner = CliRunner()
    r = runner.invoke(app, ["install-hooks", "--dry-run"])
    assert r.exit_code == 0, r.output
    assert not (fake_home / ".claude" / "settings.json").exists()
    # The dry-run output should be valid JSON containing our hook names.
    out_json = json.loads(r.output)
    names = [h["name"] for h in out_json["hooks"]["SessionStart"]]
    assert "chub-register-readonly" in names
