"""ULID-based ids: s_<26-char ulid> for sessions, hr_<26-char ulid> for hub-runs."""

from __future__ import annotations

import ulid


def new_session_id() -> str:
    return f"s_{ulid.new().str}"


def new_hub_run_id() -> str:
    return f"hr_{ulid.new().str}"
