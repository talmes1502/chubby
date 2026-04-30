"""``chubby install-hooks`` — idempotently merge chubby's SessionStart and Stop
hooks into ``~/.claude/settings.json``.

Claude's hooks schema requires each event entry to be a matcher group of
the form ``{"matcher": "<glob>", "hooks": [{"type": "command", "command": ...}]}``.
The legacy flat ``{"name": "...", "command": "..."}`` shape that earlier
chubby versions wrote is rejected at startup with a "Settings Error" dialog,
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

# Each chubby hook is identified by a stable ``name`` field on the inner
# ``hooks`` entry so we can find and replace existing chubby hooks
# idempotently across upgrades.
_REGISTER_NAME = "chubby-register-readonly"
_MARK_IDLE_NAME = "chubby-mark-idle"

# Legacy hook names we wrote before the chub→chubby rename. We drop these
# on every install so existing chub-named entries get replaced with
# chubby-named entries when the user re-runs ``chubby install-hooks``.
_LEGACY_REGISTER_NAME = "chub-register-readonly"
_LEGACY_MARK_IDLE_NAME = "chub-mark-idle"

CHUBBY_HOOKS: dict[str, list[dict[str, Any]]] = {
    "SessionStart": [
        {
            "matcher": "",
            "hooks": [
                {
                    "type": "command",
                    "name": _REGISTER_NAME,
                    "command": (
                        "chubby register-readonly "
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
                        "chubby mark-idle "
                        "--claude-session-id $CLAUDE_SESSION_ID || true"
                    ),
                }
            ],
        }
    ],
}

_CHUBBY_NAMES = {
    _REGISTER_NAME,
    _MARK_IDLE_NAME,
    _LEGACY_REGISTER_NAME,
    _LEGACY_MARK_IDLE_NAME,
}


def _migrate_legacy_entries(entries: list[Any]) -> list[Any]:
    """Drop legacy chub-named and flat-shape chubby entries.

    Handles both ``{"name": ..., "command": ...}`` flat-shape entries and
    matcher-group entries whose inner hook ``name`` is an owned name (whether
    the legacy ``chub-*`` or current ``chubby-*`` form). User-authored entries
    are preserved untouched. Only chubby-owned entries (matched by ``name``)
    are removed so the merge below can reinstall them in the current schema.
    """
    out: list[Any] = []
    for e in entries:
        if not isinstance(e, dict):
            out.append(e)
            continue
        # Flat-shape legacy entry: {"name": "...", "command": "..."}
        if (
            "matcher" not in e
            and isinstance(e.get("name"), str)
            and e["name"] in _CHUBBY_NAMES
        ):
            continue
        # Matcher-group entry whose inner hook is an owned (incl. legacy) name.
        inner = e.get("hooks")
        if isinstance(inner, list) and any(
            isinstance(h, dict)
            and isinstance(h.get("name"), str)
            and h["name"] in _CHUBBY_NAMES
            for h in inner
        ):
            continue
        out.append(e)
    return out


def _has_chubby_hook(group: dict[str, Any], name: str) -> bool:
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
    for event, entries in CHUBBY_HOOKS.items():
        existing = hooks.setdefault(event, [])
        if not isinstance(existing, list):
            existing = []
            hooks[event] = existing
        # Strip out any legacy chub-named or flat-shape chubby entries
        # before re-merging.
        existing[:] = _migrate_legacy_entries(existing)
        for new_group in entries:
            target_name = new_group["hooks"][0]["name"]
            if any(_has_chubby_hook(g, target_name) for g in existing):
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
        f"chubby hooks installed in {SETTINGS} (backup at {SETTINGS}.bak)"
    )
