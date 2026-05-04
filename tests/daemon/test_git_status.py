"""Tests for the branch ahead/behind sweep that drives the rail glyph.

We use a real ``git init`` in ``tmp_path`` (cheap, deterministic, no
network). A "remote" is faked by initialising a bare repo as origin
and pushing/pulling to set up the upstream tracking shape that
``git rev-list --left-right --count @{u}...HEAD`` needs.
"""

from __future__ import annotations

import asyncio
import shutil
import subprocess
from pathlib import Path
from typing import Any

import pytest

from chubby.daemon import git_status
from chubby.daemon.main import _sweep_git_status_once
from chubby.daemon.registry import Registry
from chubby.daemon.session import SessionKind


def _git(cwd: Path, *args: str) -> None:
    """Run a git command in ``cwd`` with stable identity & disabled
    GPG so tests don't depend on the developer's git config."""
    env = {
        "GIT_AUTHOR_NAME": "t",
        "GIT_AUTHOR_EMAIL": "t@t",
        "GIT_COMMITTER_NAME": "t",
        "GIT_COMMITTER_EMAIL": "t@t",
        "GIT_CONFIG_GLOBAL": "/dev/null",
        "GIT_CONFIG_SYSTEM": "/dev/null",
        "PATH": "/usr/bin:/bin:/usr/local/bin",
    }
    subprocess.run(
        ["git", "-C", str(cwd), *args],
        check=True,
        capture_output=True,
        env=env,
    )


def _make_repo_with_upstream(tmp_path: Path) -> Path:
    """Create ``tmp_path/work`` cloned from ``tmp_path/origin.git`` with
    one commit pushed. Returns the path to the working tree."""
    if not shutil.which("git"):
        pytest.skip("git not available on PATH")
    origin = tmp_path / "origin.git"
    _git(tmp_path, "init", "--bare", str(origin))
    work = tmp_path / "work"
    work.mkdir()
    _git(work, "init", "-b", "main")
    _git(work, "remote", "add", "origin", str(origin))
    (work / "f").write_text("a")
    _git(work, "add", "f")
    _git(work, "commit", "-m", "first")
    _git(work, "push", "-u", "origin", "main")
    return work


class _FakeSubs:
    def __init__(self) -> None:
        self.broadcasts: list[tuple[str, dict[str, Any]]] = []

    async def broadcast(self, method: str, params: dict[str, Any]) -> None:
        self.broadcasts.append((method, params))


async def test_ahead_behind_clean_repo(tmp_path: Path) -> None:
    """A repo whose HEAD matches its upstream is ahead=0, behind=0."""
    work = _make_repo_with_upstream(tmp_path)
    result = await git_status.ahead_behind(str(work))
    assert result == (0, 0)


async def test_ahead_behind_after_local_commit(tmp_path: Path) -> None:
    """Add one commit locally → ahead=1, behind=0."""
    work = _make_repo_with_upstream(tmp_path)
    (work / "f").write_text("b")
    _git(work, "add", "f")
    _git(work, "commit", "-m", "second")
    result = await git_status.ahead_behind(str(work))
    assert result == (1, 0)


async def test_ahead_behind_returns_none_for_no_upstream(tmp_path: Path) -> None:
    """A repo whose current branch has no configured upstream returns
    ``None`` (the rail glyph is suppressed)."""
    if not shutil.which("git"):
        pytest.skip("git not available on PATH")
    work = tmp_path / "noupstream"
    work.mkdir()
    _git(work, "init", "-b", "main")
    (work / "f").write_text("a")
    _git(work, "add", "f")
    _git(work, "commit", "-m", "first")
    result = await git_status.ahead_behind(str(work))
    assert result is None


async def test_ahead_behind_returns_none_for_non_repo(tmp_path: Path) -> None:
    """A directory that isn't a git working tree returns ``None``."""
    if not shutil.which("git"):
        pytest.skip("git not available on PATH")
    result = await git_status.ahead_behind(str(tmp_path))
    assert result is None


async def test_sweep_emits_event_when_counts_change(tmp_path: Path) -> None:
    """Single sweep tick: registers a session, runs the sweep, asserts
    a ``session_git_status_changed`` event with the right counts."""
    work = _make_repo_with_upstream(tmp_path)
    # Diverge from origin so ahead=1.
    (work / "f").write_text("b")
    _git(work, "add", "f")
    _git(work, "commit", "-m", "second")

    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="ws", kind=SessionKind.WRAPPED, cwd=str(work))
    subs.broadcasts.clear()  # ignore session_added

    changed = await _sweep_git_status_once(reg)
    assert changed == 1

    git_events = [p for m, p in subs.broadcasts if m == "session_git_status_changed"]
    assert len(git_events) == 1
    ev = git_events[0]
    assert ev["id"] == s.id
    assert ev["ahead"] == 1
    assert ev["behind"] == 0


async def test_sweep_does_not_re_emit_for_unchanged_counts(tmp_path: Path) -> None:
    """A second sweep with no repo changes must not emit again — keeps
    the broadcast bus quiet on the steady state."""
    work = _make_repo_with_upstream(tmp_path)
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    await reg.register(name="ws", kind=SessionKind.WRAPPED, cwd=str(work))

    await _sweep_git_status_once(reg)  # first tick — emits (None → 0,0)
    subs.broadcasts.clear()
    second = await _sweep_git_status_once(reg)
    assert second == 0
    git_events = [m for m, _ in subs.broadcasts if m == "session_git_status_changed"]
    assert git_events == []


async def test_sweep_skips_dead_sessions(tmp_path: Path) -> None:
    """DEAD sessions get no git status — wrapper's gone, the cwd
    might be a stale worktree."""
    work = _make_repo_with_upstream(tmp_path)
    subs = _FakeSubs()
    reg = Registry(hub_run_id="hr_t", subs=subs)  # type: ignore[arg-type]
    s = await reg.register(name="ws", kind=SessionKind.WRAPPED, cwd=str(work))
    from chubby.daemon.session import SessionStatus
    await reg.update_status(s.id, SessionStatus.DEAD)
    subs.broadcasts.clear()

    changed = await _sweep_git_status_once(reg)
    assert changed == 0
