"""Branch ahead/behind detection for the rail.

Each non-DEAD session's cwd is polled periodically; when the count of
commits relative to the upstream changes, a ``session_git_status_changed``
event fires so the TUI can update its rail glyph.

This is intentionally minimal:
- No caching of commit shas — we always re-run the rev-list.
- No DB persistence — counts are transient state.
- ``None`` is the natural "no upstream / not a repo" sentinel; we
  don't broadcast a missing-upstream event because there's nothing
  for the rail to render.
"""

from __future__ import annotations

import asyncio
import logging

log = logging.getLogger(__name__)


# A run that takes longer than this is treated as a transient failure
# (slow disk, big repo) and skipped for that tick — no event fires.
_GIT_TIMEOUT_S = 5.0


async def ahead_behind(cwd: str) -> tuple[int, int] | None:
    """Return ``(ahead, behind)`` commit counts for the branch at
    ``cwd`` relative to its configured upstream, or ``None`` when:

    - the path doesn't resolve to a git working tree
    - the current branch has no upstream configured (``@{u}``
      raises an error from ``git rev-list``)
    - any of the git commands time out, fail, or produce
      unexpected output

    The two-counter form comes from
    ``git rev-list --left-right --count @{u}...HEAD`` which prints
    ``<behind>\\t<ahead>``. We deliberately don't run ``git fetch``
    here — that would be a network call and would make the sweep
    slow; users can ``git fetch`` themselves and the next tick
    picks up the new counts.
    """
    try:
        proc = await asyncio.create_subprocess_exec(
            "git", "-C", cwd, "rev-list", "--left-right", "--count",
            "@{u}...HEAD",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
    except FileNotFoundError:
        # git not installed — nothing more we can do.
        return None
    try:
        stdout, _stderr = await asyncio.wait_for(
            proc.communicate(), timeout=_GIT_TIMEOUT_S
        )
    except asyncio.TimeoutError:
        try:
            proc.kill()
        except ProcessLookupError:
            pass
        return None
    if proc.returncode != 0:
        return None
    parts = stdout.decode("utf-8", errors="replace").strip().split()
    if len(parts) != 2:
        return None
    try:
        behind = int(parts[0])
        ahead = int(parts[1])
    except ValueError:
        return None
    return ahead, behind
