"""Tests for ``chubby install-hooks`` (idempotent merge into ~/.claude/settings.json)."""

from __future__ import annotations

import importlib
import json
from pathlib import Path

import pytest
from typer.testing import CliRunner


def _reload_module(monkeypatch: pytest.MonkeyPatch, fake_home: Path) -> None:
    """Reload modules that captured ``Path.home()`` at import time."""
    monkeypatch.setenv("HOME", str(fake_home))
    from chubby.cli import main as cli_main
    from chubby.cli.commands import install_hooks

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


def test_install_hooks_idempotent(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    _reload_module(monkeypatch, fake_home)

    from chubby.cli.main import app

    runner = CliRunner()
    r1 = runner.invoke(app, ["install-hooks", "--auto-register"])
    assert r1.exit_code == 0, r1.output
    r2 = runner.invoke(app, ["install-hooks", "--auto-register"])
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
    assert _chub_inner_names(session_start).count("chubby-register-readonly") == 1
    assert _chub_inner_names(stop).count("chubby-mark-idle") == 1


def test_install_hooks_default_skips_session_start(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Without ``--auto-register``, only the Stop hook is installed —
    raw ``claude`` runs no longer auto-register as readonly sessions.
    """
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    _reload_module(monkeypatch, fake_home)

    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["install-hooks"])
    assert r.exit_code == 0, r.output

    settings = json.loads((fake_home / ".claude" / "settings.json").read_text())
    hooks = settings["hooks"]
    # Stop hook still installed (needed for awaiting_user trigger on
    # chubby-launched sessions).
    assert "chubby-mark-idle" in _chub_inner_names(hooks["Stop"])
    # SessionStart is either missing entirely or contains no chubby entry.
    session_start = hooks.get("SessionStart", [])
    assert "chubby-register-readonly" not in _chub_inner_names(session_start)


def test_install_hooks_removes_session_start_on_downgrade(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A user who previously ran ``--auto-register`` and now re-runs
    without the flag should have the SessionStart chubby entry removed
    automatically — no manual settings.json editing required."""
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    _reload_module(monkeypatch, fake_home)

    from chubby.cli.main import app

    runner = CliRunner()
    runner.invoke(app, ["install-hooks", "--auto-register"])
    settings = json.loads((fake_home / ".claude" / "settings.json").read_text())
    assert "chubby-register-readonly" in _chub_inner_names(settings["hooks"]["SessionStart"])

    runner.invoke(app, ["install-hooks"])
    settings = json.loads((fake_home / ".claude" / "settings.json").read_text())
    session_start = settings.get("hooks", {}).get("SessionStart", [])
    assert "chubby-register-readonly" not in _chub_inner_names(session_start)
    # Stop hook stays put.
    assert "chubby-mark-idle" in _chub_inner_names(settings["hooks"]["Stop"])


def test_install_hooks_downgrade_preserves_user_session_start_entries(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Removing chubby's SessionStart entry must leave the user's own
    SessionStart hooks intact."""
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    claude_dir = fake_home / ".claude"
    claude_dir.mkdir()
    pre_existing = {
        "hooks": {
            "SessionStart": [
                {
                    "matcher": "",
                    "hooks": [
                        {
                            "type": "command",
                            "name": "user-custom",
                            "command": "echo custom",
                        }
                    ],
                }
            ]
        }
    }
    (claude_dir / "settings.json").write_text(json.dumps(pre_existing))

    _reload_module(monkeypatch, fake_home)
    from chubby.cli.main import app

    runner = CliRunner()
    runner.invoke(app, ["install-hooks", "--auto-register"])
    runner.invoke(app, ["install-hooks"])

    settings = json.loads((claude_dir / "settings.json").read_text())
    session_start = settings["hooks"]["SessionStart"]
    names = _chub_inner_names(session_start)
    assert "user-custom" in names
    assert "chubby-register-readonly" not in names


def test_install_hooks_preserves_existing(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    claude_dir = fake_home / ".claude"
    claude_dir.mkdir()
    # Pre-existing user hook in the legacy flat shape — chubby must leave
    # user entries alone even if they don't match the current schema.
    pre_existing = {
        "hooks": {"SessionStart": [{"name": "user-custom", "command": "echo custom"}]},
        "theme": "dark",
    }
    (claude_dir / "settings.json").write_text(json.dumps(pre_existing))

    _reload_module(monkeypatch, fake_home)
    from chubby.cli.main import app

    runner = CliRunner()
    r = runner.invoke(app, ["install-hooks", "--auto-register"])
    assert r.exit_code == 0, r.output

    settings = json.loads((claude_dir / "settings.json").read_text())
    session_start = settings["hooks"]["SessionStart"]
    # User entry preserved untouched.
    user_entries = [
        e for e in session_start if isinstance(e, dict) and e.get("name") == "user-custom"
    ]
    assert len(user_entries) == 1
    assert user_entries[0]["command"] == "echo custom"
    assert "chubby-register-readonly" in _chub_inner_names(session_start)
    assert settings["theme"] == "dark"
    # A backup of the pre-existing settings.json was created.
    assert (claude_dir / "settings.json.bak").exists()


def test_install_hooks_migrates_legacy_chub_entries(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A settings.json with chubby's old flat-shape entries must be migrated
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
                    "name": "chubby-register-readonly",
                    "command": "chubby register-readonly --claude-session-id $X --cwd $Y || true",
                }
            ],
            "Stop": [
                {
                    "name": "chubby-mark-idle",
                    "command": "chubby mark-idle --claude-session-id $X || true",
                }
            ],
        }
    }
    (claude_dir / "settings.json").write_text(json.dumps(legacy))

    _reload_module(monkeypatch, fake_home)
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["install-hooks", "--auto-register"])
    assert r.exit_code == 0, r.output

    settings = json.loads((claude_dir / "settings.json").read_text())
    session_start = settings["hooks"]["SessionStart"]
    stop = settings["hooks"]["Stop"]
    # Legacy chubby entries gone — only the new matcher-group shape remains.
    assert all("matcher" in g for g in session_start)
    assert all("matcher" in g for g in stop)
    assert _chub_inner_names(session_start) == ["chubby-register-readonly"]
    assert _chub_inner_names(stop) == ["chubby-mark-idle"]


def test_install_hooks_migrates_anonymous_legacy_entries(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Regression test for the split-brain hook bug.

    A pre-rename install left settings.json with anonymous matcher-group
    entries calling the now-stale ``chub`` binary (no ``name`` field).
    The earlier migration only matched by ``name``, so those entries
    survived every ``chubby install-hooks`` run, and Stop hooks fired
    against the dead ``chub`` socket — making AWAITING_USER never
    trigger and chubby's force-redraw never fire (visible as ghost text
    in the input box). The migration must now detect ownership by
    command-string content too, so anonymous legacy entries are
    evicted and replaced with the named chubby entries."""
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    claude_dir = fake_home / ".claude"
    claude_dir.mkdir()
    anonymous_legacy = {
        "hooks": {
            "SessionStart": [
                {
                    "matcher": "",
                    "hooks": [
                        {
                            "type": "command",
                            "command": (
                                "chub register-readonly "
                                "--claude-session-id $CLAUDE_SESSION_ID "
                                "--cwd $PWD || true"
                            ),
                        }
                    ],
                }
            ],
            "Stop": [
                {
                    "matcher": "",
                    "hooks": [
                        {
                            "type": "command",
                            "command": (
                                "chub mark-idle --claude-session-id $CLAUDE_SESSION_ID || true"
                            ),
                        }
                    ],
                }
            ],
        }
    }
    (claude_dir / "settings.json").write_text(json.dumps(anonymous_legacy))

    _reload_module(monkeypatch, fake_home)
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["install-hooks", "--auto-register"])
    assert r.exit_code == 0, r.output

    settings = json.loads((claude_dir / "settings.json").read_text())
    session_start = settings["hooks"]["SessionStart"]
    stop = settings["hooks"]["Stop"]
    # Anonymous chub entries gone — only the new chubby-named entries.
    assert _chub_inner_names(session_start) == ["chubby-register-readonly"]
    assert _chub_inner_names(stop) == ["chubby-mark-idle"]
    # No leftover entries pointing at the dead ``chub`` binary.
    flat = json.dumps(settings)
    assert "chub register-readonly" not in flat
    assert "chub mark-idle" not in flat


def test_install_hooks_writes_stdin_based_commands(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """The hook commands must NOT pass ``--claude-session-id``: that
    flag relied on ``$CLAUDE_SESSION_ID`` which Claude Code never
    exports, so the substitution evaluated to empty and typer rejected
    the bare flag — every Stop hook silently failed and chubby's
    AWAITING_USER redraw never triggered.

    Claude Code's actual hook contract is JSON on stdin. The commands
    we install must read from stdin, so they need NO arguments.
    """
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    _reload_module(monkeypatch, fake_home)
    from chubby.cli.main import app

    r = CliRunner().invoke(app, ["install-hooks", "--auto-register"])
    assert r.exit_code == 0, r.output

    settings = json.loads((fake_home / ".claude" / "settings.json").read_text())
    flat = json.dumps(settings)
    # The broken flag must not appear anywhere in the installed hooks.
    assert "--claude-session-id" not in flat, (
        "install-hooks must not write --claude-session-id flags; "
        "Claude Code passes session id on stdin, not via env var"
    )
    assert "$CLAUDE_SESSION_ID" not in flat, (
        "install-hooks must not reference $CLAUDE_SESSION_ID; "
        "that env var doesn't exist in Claude Code's hook env"
    )
    # And the commands we actually want are present.
    assert "chubby register-readonly" in flat
    assert "chubby mark-idle" in flat


def test_install_hooks_dry_run_does_not_write(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    _reload_module(monkeypatch, fake_home)
    from chubby.cli.main import app

    runner = CliRunner()
    r = runner.invoke(app, ["install-hooks", "--dry-run", "--auto-register"])
    assert r.exit_code == 0, r.output
    assert not (fake_home / ".claude" / "settings.json").exists()
    # The dry-run output should be valid JSON containing our hook names.
    out_json = json.loads(r.output)
    assert "chubby-register-readonly" in _chub_inner_names(out_json["hooks"]["SessionStart"])
