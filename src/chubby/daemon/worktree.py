"""Git worktree helpers for branch-isolated chubby sessions.

Each session that opts into a branch (`chubby spawn --branch X`) gets
its own worktree under ``~/.claude/chubby/worktrees/<repo-hash>/<branch>``,
so multiple sessions on the same repo can edit independently without
stepping on each other's working tree. The repo hash is a sha256 of
the repo's absolute root path, truncated to 12 chars — short enough
to keep paths under macOS's 104-byte ``sun_path`` limit while staying
unique in practice.

We deliberately keep the abstraction thin: every helper shells out to
``git`` and returns a ``Path`` or ``None``. No in-memory cache, no
manifest file — the truth lives in ``git worktree list``. That makes
recovery from a crashed daemon trivial: restart, re-resolve the
worktree path from the same inputs, and ``git worktree`` already knows
about the existing worktree.
"""

from __future__ import annotations

import asyncio
import hashlib
import logging
import os
import re
import shutil
from pathlib import Path

log = logging.getLogger(__name__)


# Git allows ``/`` in branch names (e.g. ``feature/login``) but worktree
# directories should be flat to avoid mixing with chubby's per-repo
# subdir layout. Re-encode anything outside ``[A-Za-z0-9._-]`` to ``_``.
_BRANCH_PATH_UNSAFE = re.compile(r"[^A-Za-z0-9._-]+")


# Don't allow worktree subprocess calls to hang the daemon — git is
# usually instant but can stall on a corrupted repo. 10s is generous
# enough for a slow first ``git worktree add`` on a big repo without
# blocking the spawn flow indefinitely.
_GIT_TIMEOUT_S = 10.0


async def _run_git(
    cwd: str | Path | None, *args: str, timeout_s: float = _GIT_TIMEOUT_S
) -> tuple[int, str, str]:
    """Run ``git [-C cwd] <args>``. Returns ``(rc, stdout, stderr)``.

    rc is -1 if git wasn't found or the call timed out — callers
    treat both as "couldn't determine, fall back to non-worktree
    spawn".
    """
    argv = ["git"]
    if cwd is not None:
        argv += ["-C", str(cwd)]
    argv += list(args)
    try:
        proc = await asyncio.create_subprocess_exec(
            *argv,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
    except FileNotFoundError:
        return -1, "", "git not found on PATH"
    try:
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout_s)
    except TimeoutError:
        try:
            proc.kill()
        except ProcessLookupError:
            pass
        return -1, "", f"git timed out after {timeout_s}s"
    return (
        proc.returncode if proc.returncode is not None else -1,
        stdout.decode("utf-8", errors="replace"),
        stderr.decode("utf-8", errors="replace"),
    )


async def is_git_repo(path: str | Path) -> bool:
    """True iff ``path`` is inside a git working tree."""
    rc, _, _ = await _run_git(path, "rev-parse", "--is-inside-work-tree")
    return rc == 0


async def repo_root(path: str | Path) -> Path | None:
    """Return the absolute root of the git working tree containing
    ``path``, or ``None`` if ``path`` isn't in one."""
    rc, out, _ = await _run_git(path, "rev-parse", "--show-toplevel")
    if rc != 0:
        return None
    line = out.strip()
    if not line:
        return None
    return Path(line)


def worktree_root() -> Path:
    """Where chubby keeps its worktrees. ``CHUBBY_WORKTREES_ROOT``
    overrides for tests; the default lives next to the daemon's
    state.db so they share lifecycle and disk."""
    env = os.environ.get("CHUBBY_WORKTREES_ROOT")
    if env:
        return Path(env)
    return Path.home() / ".claude" / "chubby" / "worktrees"


def _repo_hash(repo_root_path: Path) -> str:
    """Stable 12-char identifier for a repo, derived from its
    absolute path. Same repo path → same hash → same worktree
    parent dir, so concurrent sessions on the same repo cluster
    together on disk."""
    h = hashlib.sha256(str(repo_root_path).encode("utf-8")).hexdigest()
    return h[:12]


def _safe_branch_dirname(branch: str) -> str:
    """Path-safe form of a branch name. ``feature/login`` becomes
    ``feature_login`` so the worktree filesystem layout stays flat;
    git itself still tracks the original branch name."""
    safe = _BRANCH_PATH_UNSAFE.sub("_", branch).strip("_")
    return safe or "_"


def worktree_path(repo_root_path: Path, branch: str) -> Path:
    """The on-disk location chubby will use for the worktree of
    ``branch`` in ``repo_root_path``. Pure function — no I/O."""
    return worktree_root() / _repo_hash(repo_root_path) / _safe_branch_dirname(branch)


async def branch_exists(repo_root_path: Path, branch: str) -> bool:
    """True iff ``branch`` exists locally in ``repo_root_path``."""
    rc, _, _ = await _run_git(
        repo_root_path,
        "show-ref",
        "--verify",
        "--quiet",
        f"refs/heads/{branch}",
    )
    return rc == 0


async def worktree_exists(path: Path) -> bool:
    """True iff ``path`` is an active worktree according to ``git
    worktree list``. Checks against the canonical list rather than
    the filesystem, so a leftover empty directory after a manual
    ``rm -rf`` is correctly detected as 'not a worktree'."""
    if not path.exists():
        return False
    rc, out, _ = await _run_git(path, "worktree", "list", "--porcelain")
    if rc != 0:
        return False
    needle = f"worktree {path.resolve()}"
    return needle in out


async def add_worktree(repo_root_path: Path, branch: str, base: str = "HEAD") -> Path:
    """Create a worktree for ``branch`` at the chubby-managed path.

    If ``branch`` already exists locally, checks it out into the
    worktree without ``-b``. Otherwise creates the branch from
    ``base`` (default: HEAD of the source repo). If the worktree
    path already corresponds to an active worktree, return its
    path unchanged (idempotent on re-spawn).

    Raises ``WorktreeError`` on git failure with the stderr tail
    so the caller can surface a useful message to the rail.
    """
    target = worktree_path(repo_root_path, branch)
    if await worktree_exists(target):
        return target
    target.parent.mkdir(parents=True, exist_ok=True)
    if await branch_exists(repo_root_path, branch):
        rc, _, stderr = await _run_git(repo_root_path, "worktree", "add", str(target), branch)
    else:
        rc, _, stderr = await _run_git(
            repo_root_path, "worktree", "add", "-b", branch, str(target), base
        )
    if rc != 0:
        raise WorktreeError(
            f"git worktree add failed (branch={branch!r}, base={base!r}): "
            f"{stderr.strip() or 'unknown error'}"
        )
    return target


async def remove_worktree(path: Path) -> None:
    """Remove the worktree at ``path`` from git's index AND from disk.

    Best-effort: if ``git worktree remove`` fails (e.g., uncommitted
    changes), we log and fall back to ``rm -rf`` so the directory
    doesn't accumulate. Git's worktree metadata gets cleaned by
    ``git worktree prune`` automatically on the next command.
    """
    if not path.exists():
        return
    # Try the clean path first.
    rc, _, stderr = await _run_git(path, "worktree", "remove", "--force", str(path))
    if rc == 0:
        return
    log.warning(
        "git worktree remove failed for %s: %s; falling back to rm -rf",
        path,
        stderr.strip() or "unknown error",
    )
    # Fallback: nuke the dir so we don't leak disk. ``git worktree
    # prune`` (next git command) will reap the stale registration.
    try:
        shutil.rmtree(path, ignore_errors=True)
    except OSError as e:
        log.warning("rm -rf %s also failed: %s", path, e)


async def resolve_pr_branch(repo_root_path: Path, pr_number: int) -> str | None:
    """Resolve a GitHub PR number to its head branch name via the
    ``gh`` CLI. Returns ``None`` when ``gh`` isn't installed/
    authenticated or the PR doesn't exist — the caller falls back to
    treating the explicit ``branch`` arg (if any) as authoritative.
    """
    try:
        proc = await asyncio.create_subprocess_exec(
            "gh",
            "pr",
            "view",
            str(pr_number),
            "--json",
            "headRefName",
            cwd=str(repo_root_path),
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
    except FileNotFoundError:
        return None
    try:
        stdout, _stderr = await asyncio.wait_for(proc.communicate(), timeout=_GIT_TIMEOUT_S)
    except TimeoutError:
        try:
            proc.kill()
        except ProcessLookupError:
            pass
        return None
    if proc.returncode != 0:
        return None
    import json

    try:
        data = json.loads(stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError:
        return None
    head = data.get("headRefName")
    return head if isinstance(head, str) and head else None


class WorktreeError(Exception):
    """Raised when a worktree operation fails. The message is
    user-facing — it lands in the daemon's session-spawn error
    response and the TUI surfaces it in the rail."""
