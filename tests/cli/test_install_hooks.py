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


def _chub_inner_names(groups: list[dict]) -> list[str]:
    """Flatten matcher groups to the inner hook ``name`` fields."""
    out: list[str] = []
    for g in groups:
        for h in g.get("hooks", []):
            n = h.get("name")
            if isinstance(n, str):
                out.append(n)
    return out


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
    # Each matcher group must have the matcher+hooks shape Claude expects.
    for group in session_start + stop:
        assert "matcher" in group and "hooks" in group
        for inner in group["hooks"]:
            assert inner.get("type") == "command"
    assert _chub_inner_names(session_start).count("chub-register-readonly") == 1
    assert _chub_inner_names(stop).count("chub-mark-idle") == 1


def test_install_hooks_preserves_existing(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    claude_dir = fake_home / ".claude"
    claude_dir.mkdir()
    # Pre-existing user hook in the legacy flat shape — chub must leave
    # user entries alone even if they don't match the current schema.
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
    session_start = settings["hooks"]["SessionStart"]
    # User entry preserved untouched.
    user_entries = [
        e for e in session_start if isinstance(e, dict) and e.get("name") == "user-custom"
    ]
    assert len(user_entries) == 1
    assert user_entries[0]["command"] == "echo custom"
    assert "chub-register-readonly" in _chub_inner_names(session_start)
    assert settings["theme"] == "dark"
    # A backup of the pre-existing settings.json was created.
    assert (claude_dir / "settings.json.bak").exists()


def test_install_hooks_migrates_legacy_chub_entries(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A settings.json with chub's old flat-shape entries must be migrated
    to the matcher+hooks shape on the next ``install-hooks`` run, otherwise
    Claude blocks every session with a Settings Error dialog."""
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    claude_dir = fake_home / ".claude"
    claude_dir.mkdir()
    legacy = {
        "hooks": {
            "SessionStart": [
                {
                    "name": "chub-register-readonly",
                    "command": "chub register-readonly --claude-session-id $X --cwd $Y || true",
                }
            ],
            "Stop": [
                {
                    "name": "chub-mark-idle",
                    "command": "chub mark-idle --claude-session-id $X || true",
                }
            ],
        }
    }
    (claude_dir / "settings.json").write_text(json.dumps(legacy))

    _reload_module(monkeypatch, fake_home)
    from chub.cli.main import app

    r = CliRunner().invoke(app, ["install-hooks"])
    assert r.exit_code == 0, r.output

    settings = json.loads((claude_dir / "settings.json").read_text())
    session_start = settings["hooks"]["SessionStart"]
    stop = settings["hooks"]["Stop"]
    # Legacy chub entries gone — only the new matcher-group shape remains.
    assert all("matcher" in g for g in session_start)
    assert all("matcher" in g for g in stop)
    assert _chub_inner_names(session_start) == ["chub-register-readonly"]
    assert _chub_inner_names(stop) == ["chub-mark-idle"]


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
    assert "chub-register-readonly" in _chub_inner_names(
        out_json["hooks"]["SessionStart"]
    )
