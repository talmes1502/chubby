"""Filesystem layout. CHUBBY_HOME (legacy: CHUB_HOME) overrides ~/.claude/chubby for tests."""

from __future__ import annotations

import os
from pathlib import Path


def chubby_env(name: str, default: str | None = None) -> str | None:
    """Read CHUBBY_<name> with CHUB_<name> as fallback for backward compat."""
    v = os.environ.get(f"CHUBBY_{name}")
    if v is not None:
        return v
    return os.environ.get(f"CHUB_{name}", default)


def hub_home() -> Path:
    env = chubby_env("HOME")
    if env:
        return Path(env)
    new_path = Path.home() / ".claude" / "chubby"
    legacy = Path.home() / ".claude" / "hub"
    # One-time migration: if the legacy dir exists and the new one
    # doesn't, just rename it. We don't want to leave both in place.
    if legacy.exists() and not new_path.exists():
        try:
            legacy.rename(new_path)
        except OSError:
            # If rename fails (cross-device, perms), fall back to new_path
            # which will be created fresh.
            pass
    return new_path


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
