"""Spawn presets — saved templates for ``chubby spawn``.

Each preset records the values you'd otherwise type into the spawn
modal: name (templateable), cwd, optional branch, tags. Templates
support ``{date}`` (today, ``YYYY-MM-DD``) and ``{name}`` (the
preset's own name) substitution so a preset like
``{"name": "wip-{date}", "cwd": "~/myrepo", "branch": "wip-{date}"}``
spawns a fresh worktree branch every day.

Storage: ``~/.claude/chubby/presets.json`` (a list of preset dicts).
Lives in the same dir as ``state.db`` / ``folders.json`` to keep
chubby's persistent state under one parent. CHUBBY_HOME overrides
for tests via the standard ``paths.hub_home()`` helper.

Pure load / save / list / delete — no daemon dependencies; the
TUI can hit the same file via a daemon RPC.
"""

from __future__ import annotations

import json
import logging
import os
from dataclasses import asdict, dataclass, field
from datetime import date
from pathlib import Path
from typing import Any

from chubby.daemon import paths

log = logging.getLogger(__name__)


@dataclass
class Preset:
    """One saved spawn template. ``name`` is the lookup key (must be
    unique). All other fields are optional — a minimal preset is just
    ``{"name": "web", "cwd": "~/web"}``."""
    name: str
    cwd: str = ""
    branch: str | None = None
    tags: list[str] = field(default_factory=list)
    description: str = ""

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        # Drop empty-string description / None branch from the on-disk
        # JSON so a hand-written presets.json stays minimal.
        if not d["description"]:
            d.pop("description", None)
        if d["branch"] is None:
            d.pop("branch", None)
        return d

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "Preset":
        # Tolerant parser — extra fields are dropped, missing ones use
        # defaults. Reading a presets.json from a future version that
        # adds new keys won't break older daemons.
        return cls(
            name=str(raw.get("name", "")),
            cwd=str(raw.get("cwd", "")),
            branch=raw.get("branch") if raw.get("branch") else None,
            tags=[str(t) for t in raw.get("tags", []) if isinstance(t, str)],
            description=str(raw.get("description", "")),
        )

    def render(
        self,
        *,
        overrides: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        """Resolve template substitutions and return the dict to pass
        to ``spawn_session``. ``overrides`` lets the TUI/CLI override
        any field at apply time (e.g., ``--name custom-name``).
        """
        ov = overrides or {}
        ctx = {
            "date": date.today().isoformat(),
            "name": self.name,
        }
        ctx.update(ov)

        def _subst(s: str) -> str:
            return s.format(**ctx) if s else s

        result: dict[str, Any] = {
            "name": ov.get("name") or _subst(self.name),
            "cwd": os.path.expanduser(ov.get("cwd") or _subst(self.cwd)),
            "tags": list(ov.get("tags") if isinstance(ov.get("tags"), list) else self.tags),
        }
        eff_branch = ov.get("branch") if "branch" in ov else self.branch
        if eff_branch:
            result["branch"] = _subst(str(eff_branch))
        return result


def presets_path() -> Path:
    """``~/.claude/chubby/presets.json`` (or under
    ``CHUBBY_HOME`` for tests)."""
    return paths.hub_home() / "presets.json"


def load_presets() -> list[Preset]:
    """Read ``presets.json``. Returns ``[]`` on missing/empty/invalid;
    a malformed file logs a warning but doesn't raise — the user
    keeps a working chubby and can fix the JSON at leisure.
    """
    p = presets_path()
    try:
        text = p.read_text(encoding="utf-8")
    except FileNotFoundError:
        return []
    except OSError as e:
        log.warning("presets.json read failed at %s: %s", p, e)
        return []
    if not text.strip():
        return []
    try:
        data = json.loads(text)
    except json.JSONDecodeError as e:
        log.warning("presets.json invalid JSON at %s: %s", p, e)
        return []
    if not isinstance(data, list):
        log.warning("presets.json root is not a list at %s", p)
        return []
    out: list[Preset] = []
    for item in data:
        if not isinstance(item, dict):
            continue
        ps = Preset.from_dict(item)
        if not ps.name:
            continue
        out.append(ps)
    return out


def save_presets(presets: list[Preset]) -> None:
    """Atomically rewrite the presets file with ``presets``. Names
    are deduplicated keeping the *last* occurrence so callers that
    do "load → modify → save" without explicit dedup don't end up
    with multiple entries under the same name."""
    by_name: dict[str, Preset] = {}
    for p in presets:
        by_name[p.name] = p
    rows = [p.to_dict() for p in by_name.values()]
    target = presets_path()
    target.parent.mkdir(parents=True, exist_ok=True)
    tmp = target.with_suffix(target.suffix + ".tmp")
    tmp.write_text(json.dumps(rows, indent=2) + "\n", encoding="utf-8")
    tmp.replace(target)


def upsert_preset(preset: Preset) -> None:
    """Add or replace a preset by name."""
    presets = load_presets()
    presets = [p for p in presets if p.name != preset.name]
    presets.append(preset)
    save_presets(presets)


def delete_preset(name: str) -> bool:
    """Remove a preset by name. Returns True if anything changed."""
    presets = load_presets()
    kept = [p for p in presets if p.name != name]
    if len(kept) == len(presets):
        return False
    save_presets(kept)
    return True


def get_preset(name: str) -> Preset | None:
    for p in load_presets():
        if p.name == name:
            return p
    return None
