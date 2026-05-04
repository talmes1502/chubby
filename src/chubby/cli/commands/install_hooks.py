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

# Hook commands deliberately take NO arguments and rely on Claude
# Code piping the event JSON to stdin (the actual hook contract — see
# Claude Code docs for "Hooks"). Earlier versions tried to thread the
# session id via ``--claude-session-id $CLAUDE_SESSION_ID``, but
# ``$CLAUDE_SESSION_ID`` is not exported by Claude; the substitution
# evaluated to empty, typer rejected the bare flag, and every Stop
# hook silently failed. Reading from stdin is the right interface.
_SESSION_START_GROUP: dict[str, Any] = {
    "matcher": "",
    "hooks": [
        {
            "type": "command",
            "name": _REGISTER_NAME,
            "command": "chubby register-readonly || true",
        }
    ],
}

_STOP_GROUP: dict[str, Any] = {
    "matcher": "",
    "hooks": [
        {
            "type": "command",
            "name": _MARK_IDLE_NAME,
            "command": "chubby mark-idle || true",
        }
    ],
}


def _build_hooks(*, auto_register: bool) -> dict[str, list[dict[str, Any]]]:
    """The ``Stop`` hook is always on — it's needed for the awaiting_user
    glyph + system notification to trigger on chubby-launched sessions.
    The ``SessionStart`` hook auto-registers *every* raw ``claude`` run as a
    readonly session in chubby's rail; that's noisy when you only want the
    rail to track sessions you explicitly spawned, so it's opt-in via
    ``--auto-register``."""
    out: dict[str, list[dict[str, Any]]] = {"Stop": [_STOP_GROUP]}
    if auto_register:
        out["SessionStart"] = [_SESSION_START_GROUP]
    return out


_CHUBBY_NAMES = {
    _REGISTER_NAME,
    _MARK_IDLE_NAME,
    _LEGACY_REGISTER_NAME,
    _LEGACY_MARK_IDLE_NAME,
}

# Substrings that uniquely identify a chubby-owned hook by its command
# string. Needed because some legacy installs wrote anonymous entries
# (no ``name`` field) that pre-rename ``chubby install-hooks`` couldn't
# match. Without this fallback, those entries linger forever — calling
# the dead ``chub`` binary on the OLD socket while the live daemon
# misses every Stop hook fire (which is what made the AWAITING_USER
# redraw never trigger and ghost text persist on screen).
_OWNED_COMMAND_NEEDLES = (
    "chub register-readonly",
    "chub mark-idle",
    "chubby register-readonly",
    "chubby mark-idle",
)


def _is_owned_command(cmd: Any) -> bool:
    if not isinstance(cmd, str):
        return False
    return any(needle in cmd for needle in _OWNED_COMMAND_NEEDLES)


def _migrate_legacy_entries(entries: list[Any]) -> list[Any]:
    """Drop legacy chub-named and flat-shape chubby entries.

    Detection is two-pronged:
      - by ``name`` (current and legacy chubby names), and
      - by ``command`` substring (catches anonymous legacy entries
        like ``{"command": "chub mark-idle ..."}`` written before
        we started naming hooks).

    User-authored entries are preserved untouched.
    """
    out: list[Any] = []
    for e in entries:
        if not isinstance(e, dict):
            out.append(e)
            continue
        # Flat-shape legacy entry: {"name": "...", "command": "..."}
        if "matcher" not in e:
            if isinstance(e.get("name"), str) and e["name"] in _CHUBBY_NAMES:
                continue
            if _is_owned_command(e.get("command")):
                continue
        # Matcher-group entry whose inner hook is owned by name OR by
        # command-string content.
        inner = e.get("hooks")
        if isinstance(inner, list) and any(
            isinstance(h, dict)
            and (
                (isinstance(h.get("name"), str) and h["name"] in _CHUBBY_NAMES)
                or _is_owned_command(h.get("command"))
            )
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
    return any(isinstance(h, dict) and h.get("name") == name for h in inner)


def run(
    dry_run: bool = typer.Option(
        False, "--dry-run", help="Print resulting settings.json without writing"
    ),
    auto_register: bool = typer.Option(
        False,
        "--auto-register",
        help=(
            "Also install the SessionStart hook so every raw `claude` run on "
            "this machine auto-registers as a readonly session in chubby's "
            "rail. Off by default — only chubby-launched sessions appear."
        ),
    ),
) -> None:
    settings: dict[str, Any] = json.loads(SETTINGS.read_text()) if SETTINGS.exists() else {}
    hooks = settings.setdefault("hooks", {})
    desired = _build_hooks(auto_register=auto_register)
    # Always sweep both events: the user may have previously installed
    # SessionStart and is now downgrading to ``--auto-register=false``,
    # in which case we need to remove the chubby entry from SessionStart.
    for event in ("SessionStart", "Stop"):
        existing = hooks.setdefault(event, [])
        if not isinstance(existing, list):
            existing = []
            hooks[event] = existing
        # Strip out any legacy chub-named or flat-shape chubby entries
        # before re-merging.
        existing[:] = _migrate_legacy_entries(existing)
        for new_group in desired.get(event, []):
            target_name = new_group["hooks"][0]["name"]
            if any(_has_chubby_hook(g, target_name) for g in existing):
                continue
            existing.append(new_group)
        # If the event ended up empty, drop the key entirely so we don't
        # leave a stray ``"SessionStart": []`` behind that confuses readers.
        if not existing:
            hooks.pop(event, None)
    if dry_run:
        typer.echo(json.dumps(settings, indent=2))
        return
    SETTINGS.parent.mkdir(parents=True, exist_ok=True)
    if SETTINGS.exists():
        shutil.copy(SETTINGS, SETTINGS.with_suffix(".json.bak"))
    SETTINGS.write_text(json.dumps(settings, indent=2) + "\n")
    suffix = " (with auto-register)" if auto_register else ""
    typer.echo(f"chubby hooks installed in {SETTINGS}{suffix} (backup at {SETTINGS}.bak)")
