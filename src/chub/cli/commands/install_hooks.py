"""``chub install-hooks`` — idempotently merge chub's SessionStart and Stop
hooks into ``~/.claude/settings.json``.

Claude's hooks schema requires each event entry to be a matcher group of
the form ``{"matcher": "<glob>", "hooks": [{"type": "command", "command": ...}]}``.
The legacy flat ``{"name": "...", "command": "..."}`` shape that earlier
chub versions wrote is rejected at startup with a "Settings Error" dialog,
which blocks every spawned session — see the migration in
``_migrate_legacy_entries`` below for backwards compat with stale on-disk
settings.
"""

from __future__ import annotations

import json
import shutil
from pathlib import Path
from typing import Any

import typer

SETTINGS = Path.home() / ".claude" / "settings.json"

# Each chub hook is identified by a stable ``name`` field on the inner
# ``hooks`` entry so we can find and replace existing chub hooks
# idempotently across upgrades.
_REGISTER_NAME = "chub-register-readonly"
_MARK_IDLE_NAME = "chub-mark-idle"

CHUB_HOOKS: dict[str, list[dict[str, Any]]] = {
    "SessionStart": [
        {
            "matcher": "",
            "hooks": [
                {
                    "type": "command",
                    "name": _REGISTER_NAME,
                    "command": (
                        "chub register-readonly "
                        "--claude-session-id $CLAUDE_SESSION_ID --cwd $PWD || true"
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
                    "name": _MARK_IDLE_NAME,
                    "command": (
                        "chub mark-idle "
                        "--claude-session-id $CLAUDE_SESSION_ID || true"
                    ),
                }
            ],
        }
    ],
}

_CHUB_NAMES = {_REGISTER_NAME, _MARK_IDLE_NAME}


def _migrate_legacy_entries(entries: list[Any]) -> list[Any]:
    """Drop legacy flat-shape chub entries (``{"name": ..., "command": ...}``).

    User-authored entries in either shape are preserved untouched. Only
    chub-owned legacy entries (matched by ``name``) are removed so that
    the merge below can reinstall them in the current schema.
    """
    out: list[Any] = []
    for e in entries:
        if (
            isinstance(e, dict)
            and "matcher" not in e
            and isinstance(e.get("name"), str)
            and e["name"] in _CHUB_NAMES
        ):
            continue
        out.append(e)
    return out


def _has_chub_hook(group: dict[str, Any], name: str) -> bool:
    if not isinstance(group, dict):
        return False
    inner = group.get("hooks")
    if not isinstance(inner, list):
        return False
    return any(
        isinstance(h, dict) and h.get("name") == name for h in inner
    )


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
        if not isinstance(existing, list):
            existing = []
            hooks[event] = existing
        # Strip out any legacy flat-shape chub entries before re-merging.
        existing[:] = _migrate_legacy_entries(existing)
        for new_group in entries:
            target_name = new_group["hooks"][0]["name"]
            if any(_has_chub_hook(g, target_name) for g in existing):
                continue
            existing.append(new_group)
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
