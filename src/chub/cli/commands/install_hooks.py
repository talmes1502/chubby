"""``chub install-hooks`` — idempotently merge chub's SessionStart and Stop
hooks into ``~/.claude/settings.json``."""

from __future__ import annotations

import json
import shutil
from pathlib import Path
from typing import Any

import typer

SETTINGS = Path.home() / ".claude" / "settings.json"

CHUB_HOOKS: dict[str, list[dict[str, str]]] = {
    "SessionStart": [
        {
            "name": "chub-register-readonly",
            "command": (
                "chub register-readonly "
                "--claude-session-id $CLAUDE_SESSION_ID --cwd $PWD || true"
            ),
        }
    ],
    "Stop": [
        {
            "name": "chub-mark-idle",
            "command": "chub mark-idle --claude-session-id $CLAUDE_SESSION_ID || true",
        }
    ],
}


def run(
    dry_run: bool = typer.Option(
        False, "--dry-run", help="Print resulting settings.json without writing"
    ),
) -> None:
    settings: dict[str, Any] = (
        json.loads(SETTINGS.read_text()) if SETTINGS.exists() else {}
    )
    hooks = settings.setdefault("hooks", {})
    for event, entries in CHUB_HOOKS.items():
        existing = hooks.setdefault(event, [])
        existing_names = {
            h.get("name") for h in existing if isinstance(h, dict)
        }
        for h in entries:
            if h["name"] not in existing_names:
                existing.append(h)
    if dry_run:
        typer.echo(json.dumps(settings, indent=2))
        return
    SETTINGS.parent.mkdir(parents=True, exist_ok=True)
    if SETTINGS.exists():
        shutil.copy(SETTINGS, SETTINGS.with_suffix(".json.bak"))
    SETTINGS.write_text(json.dumps(settings, indent=2) + "\n")
    typer.echo(
        f"chub hooks installed in {SETTINGS} (backup at {SETTINGS}.bak)"
    )
