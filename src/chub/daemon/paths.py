"""Filesystem layout. CHUB_HOME overrides ~/.claude/hub for tests."""

from __future__ import annotations

import os
from pathlib import Path


def hub_home() -> Path:
    env = os.environ.get("CHUB_HOME")
    if env:
        return Path(env)
    return Path.home() / ".claude" / "hub"


def sock_path() -> Path:
    return hub_home() / "hub.sock"


def pid_path() -> Path:
    return hub_home() / "hub.pid"


def state_db_path() -> Path:
    return hub_home() / "state.db"


def runs_dir() -> Path:
    return hub_home() / "runs"


def config_path() -> Path:
    return hub_home() / "config.toml"
