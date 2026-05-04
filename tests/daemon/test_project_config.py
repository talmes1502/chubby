"""Tests for ``.chubby/config.json`` resolution.

Two-level priority:
1. ``<workspace>/.chubby/config.json`` (worktree-specific)
2. ``<repo>/.chubby/config.json`` (project default)

Plus an optional ``<workspace>/.chubby/config.local.json`` that layers
``before``/``after`` arrays atop the winning base.
"""

from __future__ import annotations

import json
from pathlib import Path

from chubby.daemon.project_config import load_config


def _write_json(path: Path, payload: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload), encoding="utf-8")


def test_no_config_returns_empty_lists(tmp_path: Path) -> None:
    cfg = load_config(tmp_path, tmp_path)
    assert cfg.setup == []
    assert cfg.teardown == []
    assert cfg.run == []


def test_repo_root_config_loaded(tmp_path: Path) -> None:
    repo = tmp_path / "repo"
    _write_json(
        repo / ".chubby" / "config.json",
        {"setup": ["bun install"], "teardown": ["docker compose down"], "run": ["bun dev"]},
    )
    cfg = load_config(repo, repo)
    assert cfg.setup == ["bun install"]
    assert cfg.teardown == ["docker compose down"]
    assert cfg.run == ["bun dev"]


def test_workspace_config_overrides_repo(tmp_path: Path) -> None:
    """Worktree-specific committed config wins over the repo-root
    one — useful when a particular branch deliberately runs
    different setup."""
    repo = tmp_path / "repo"
    workspace = tmp_path / "wt"
    _write_json(
        repo / ".chubby" / "config.json",
        {"setup": ["repo-setup"]},
    )
    _write_json(
        workspace / ".chubby" / "config.json",
        {"setup": ["workspace-setup"]},
    )
    cfg = load_config(workspace, repo)
    assert cfg.setup == ["workspace-setup"]


def test_local_config_before_after_overlay(tmp_path: Path) -> None:
    """The gitignored ``config.local.json`` layers ``before``/``after``
    arrays atop the committed setup/teardown."""
    repo = tmp_path / "repo"
    _write_json(
        repo / ".chubby" / "config.json",
        {"setup": ["b"], "teardown": ["t"]},
    )
    _write_json(
        repo / ".chubby" / "config.local.json",
        {
            "setup": {"before": ["pre1", "pre2"], "after": ["post1"]},
            "teardown": {"after": ["after-t"]},
        },
    )
    cfg = load_config(repo, repo)
    assert cfg.setup == ["pre1", "pre2", "b", "post1"]
    assert cfg.teardown == ["t", "after-t"]


def test_local_config_flat_replace(tmp_path: Path) -> None:
    """A local config value that's a plain list (not before/after)
    fully replaces the committed list. Useful when CI wants a
    completely different setup without touching the committed file."""
    repo = tmp_path / "repo"
    _write_json(
        repo / ".chubby" / "config.json",
        {"setup": ["committed-step"]},
    )
    _write_json(
        repo / ".chubby" / "config.local.json",
        {"setup": ["replaced"]},
    )
    cfg = load_config(repo, repo)
    assert cfg.setup == ["replaced"]


def test_malformed_json_falls_back_to_empty(tmp_path: Path) -> None:
    """A typo in the JSON should not block spawn — the user gets a
    log warning, an empty config, and life continues."""
    repo = tmp_path / "repo"
    (repo / ".chubby").mkdir(parents=True)
    (repo / ".chubby" / "config.json").write_text("{ this is invalid")
    cfg = load_config(repo, repo)
    assert cfg.setup == []
    assert cfg.teardown == []
    assert cfg.run == []


def test_non_string_entries_filtered(tmp_path: Path) -> None:
    """A list entry that isn't a non-empty string is silently dropped
    — robust to schema drift / user typos."""
    repo = tmp_path / "repo"
    _write_json(
        repo / ".chubby" / "config.json",
        {"setup": ["valid", "", 42, None, "  ", "also-valid"]},
    )
    cfg = load_config(repo, repo)
    assert cfg.setup == ["valid", "also-valid"]
