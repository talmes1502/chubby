"""Per-project lifecycle config: ``.chubby/config.json``.

Schema (mirrors Superset's, kept dead simple):

    {
      "setup":    ["./.chubby/setup.sh"],
      "teardown": ["./.chubby/teardown.sh"],
      "run":      ["bun dev"]
    }

Each array entry is one shell command line. The committed
``<repo>/.chubby/config.json`` is the source of truth; an
optional gitignored ``<workspace>/.chubby/config.local.json``
layers on top with ``before`` / ``after`` keys merging into the
committed ``setup`` / ``teardown`` arrays.

Resolution order — first match wins, no merging across levels
except for the explicit local-config overlay:

1. ``<workspace>/.chubby/config.json`` — worktree-specific committed
   config (rare, but useful when the worktree's branch deliberately
   uses a different setup than main).
2. ``<repo>/.chubby/config.json`` — the project's committed default.

Then ``<workspace>/.chubby/config.local.json`` is layered atop the
winner with ``before``/``after`` semantics.

We deliberately defer the ``~/.chubby/projects/<id>/config.json``
user-override level until users ask for it — two levels covers the
"my CI wants different setup" and "I have personal extras" cases.
"""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass, field
from pathlib import Path

log = logging.getLogger(__name__)


@dataclass
class ProjectConfig:
    """Resolved lifecycle scripts for a session's project."""
    setup: list[str] = field(default_factory=list)
    teardown: list[str] = field(default_factory=list)
    run: list[str] = field(default_factory=list)


def _read_json(path: Path) -> dict | None:
    """Return the parsed JSON dict at ``path``, or ``None`` if the
    file is missing, empty, or malformed. We log malformed configs
    but never raise — a broken config shouldn't block spawn."""
    try:
        text = path.read_text(encoding="utf-8")
    except (FileNotFoundError, NotADirectoryError):
        return None
    except OSError as e:
        log.warning(".chubby/config read failed at %s: %s", path, e)
        return None
    if not text.strip():
        return None
    try:
        data = json.loads(text)
    except json.JSONDecodeError as e:
        log.warning(".chubby/config invalid JSON at %s: %s", path, e)
        return None
    if not isinstance(data, dict):
        log.warning(".chubby/config root is not an object at %s", path)
        return None
    return data


def _string_list(v: object) -> list[str]:
    """Coerce any incoming value into a clean list of non-empty
    strings. Filters silently — a malformed entry doesn't crash the
    spawn; the bad command just won't run."""
    if not isinstance(v, list):
        return []
    out: list[str] = []
    for item in v:
        if isinstance(item, str) and item.strip():
            out.append(item)
    return out


def _apply_local_overlay(
    base: list[str], local: object, key: str
) -> list[str]:
    """Layer the local config's ``before``/``after`` arrays atop the
    base list. ``local`` is the local config's value for ``setup`` /
    ``teardown``: either a flat list (full replace) or an object with
    ``before`` and/or ``after`` keys.
    """
    if local is None:
        return base
    if isinstance(local, list):
        # Flat replace — same as if the user committed a local
        # config that owns the whole script list.
        return _string_list(local)
    if isinstance(local, dict):
        before = _string_list(local.get("before", []))
        after = _string_list(local.get("after", []))
        return [*before, *base, *after]
    log.warning("local config %r has invalid shape for %r", local, key)
    return base


def load_config(workspace_path: Path, repo_root_path: Path) -> ProjectConfig:
    """Read the project's lifecycle config. ``workspace_path`` is
    the actual cwd the wrapper will spawn into (the worktree dir
    if Phase 1's ``--branch`` was used, else the same as the repo
    root). ``repo_root_path`` is the original git working tree
    root — that's where the *committed* ``.chubby/config.json``
    lives.

    Always returns a ProjectConfig (empty if no config files
    exist). Never raises — broken configs degrade gracefully so a
    user with a typo in JSON doesn't lose the ability to spawn.
    """
    # Pick the committed base config. Worktree-specific committed
    # config wins over the repo-root one.
    base: dict | None = None
    if workspace_path != repo_root_path:
        base = _read_json(workspace_path / ".chubby" / "config.json")
    if base is None:
        base = _read_json(repo_root_path / ".chubby" / "config.json")
    if base is None:
        base = {}

    # Resolve setup/teardown/run from the base.
    setup = _string_list(base.get("setup", []))
    teardown = _string_list(base.get("teardown", []))
    run = _string_list(base.get("run", []))

    # Apply the workspace-local overlay (gitignored personal extras).
    local = _read_json(workspace_path / ".chubby" / "config.local.json")
    if local is not None:
        setup = _apply_local_overlay(setup, local.get("setup"), "setup")
        teardown = _apply_local_overlay(teardown, local.get("teardown"), "teardown")
        # ``run`` doesn't get before/after semantics — it's a list of
        # named commands, full-replace via the local config or nothing.
        if isinstance(local.get("run"), list):
            run = _string_list(local["run"])

    return ProjectConfig(setup=setup, teardown=teardown, run=run)
