"""Unit tests for the git-worktree helpers behind ``chubby spawn
--branch``. We use a real ``git init`` in ``tmp_path`` (cheap, no
network) so the tests exercise actual git semantics rather than
mocking subprocess.
"""

from __future__ import annotations

import shutil
import subprocess
from pathlib import Path

import pytest

from chubby.daemon import worktree


def _git(cwd: Path, *args: str) -> None:
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


def _init_repo(tmp_path: Path) -> Path:
    if not shutil.which("git"):
        pytest.skip("git not available on PATH")
    repo = tmp_path / "repo"
    repo.mkdir()
    _git(repo, "init", "-b", "main")
    (repo / "f").write_text("a")
    _git(repo, "add", "f")
    _git(repo, "commit", "-m", "first")
    return repo


@pytest.fixture(autouse=True)
def _isolated_worktrees_root(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> Path:
    """Redirect chubby's worktree root so tests can't leak into the
    user's real ``~/.claude/chubby/worktrees``."""
    root = tmp_path / "wt-root"
    monkeypatch.setenv("CHUBBY_WORKTREES_ROOT", str(root))
    return root


async def test_is_git_repo_yes(tmp_path: Path) -> None:
    repo = _init_repo(tmp_path)
    assert await worktree.is_git_repo(repo) is True


async def test_is_git_repo_no(tmp_path: Path) -> None:
    plain = tmp_path / "plain"
    plain.mkdir()
    assert await worktree.is_git_repo(plain) is False


async def test_repo_root_returns_canonical_path(tmp_path: Path) -> None:
    repo = _init_repo(tmp_path)
    sub = repo / "subdir"
    sub.mkdir()
    root = await worktree.repo_root(sub)
    # git's --show-toplevel returns the resolved absolute path, which
    # might differ from ``repo`` on macOS (/tmp → /private/tmp).
    assert root is not None
    assert root.resolve() == repo.resolve()


async def test_repo_root_returns_none_for_non_repo(tmp_path: Path) -> None:
    assert await worktree.repo_root(tmp_path) is None


def test_worktree_path_is_deterministic_and_safe(tmp_path: Path) -> None:
    """Same (repo, branch) pair always maps to the same path. Branch
    names with slashes get flattened so the worktree dir stays one
    level deep."""
    repo = tmp_path / "myrepo"
    p1 = worktree.worktree_path(repo, "feature/login")
    p2 = worktree.worktree_path(repo, "feature/login")
    assert p1 == p2
    # Slash flattened in the directory name; original branch name stays
    # in git's metadata (verified separately by add_worktree tests).
    assert "feature_login" in str(p1)
    assert "/" not in p1.name


async def test_branch_exists_detects_existing_local_branch(tmp_path: Path) -> None:
    repo = _init_repo(tmp_path)
    _git(repo, "checkout", "-b", "wip")
    _git(repo, "checkout", "main")
    assert await worktree.branch_exists(repo, "wip") is True
    assert await worktree.branch_exists(repo, "nope") is False


async def test_add_worktree_creates_new_branch(tmp_path: Path) -> None:
    """Branch doesn't exist → ``add`` creates it via ``git worktree
    add -b``. Worktree directory exists, has the file from main, and
    HEAD points at the new branch."""
    repo = _init_repo(tmp_path)
    target = await worktree.add_worktree(repo, "wip-feature")
    assert target.exists()
    assert (target / "f").exists()
    # The worktree's HEAD should be the new branch.
    out = subprocess.check_output(
        ["git", "-C", str(target), "rev-parse", "--abbrev-ref", "HEAD"],
        text=True,
    ).strip()
    assert out == "wip-feature"


async def test_add_worktree_checks_out_existing_branch(tmp_path: Path) -> None:
    """Branch exists → ``add`` checks it out without ``-b``. Doesn't
    error out as 'branch already exists'."""
    repo = _init_repo(tmp_path)
    _git(repo, "branch", "existing")
    target = await worktree.add_worktree(repo, "existing")
    assert target.exists()
    out = subprocess.check_output(
        ["git", "-C", str(target), "rev-parse", "--abbrev-ref", "HEAD"],
        text=True,
    ).strip()
    assert out == "existing"


async def test_add_worktree_is_idempotent(tmp_path: Path) -> None:
    """Calling add_worktree twice on the same (repo, branch) pair
    returns the same path without git errors. Important for daemon
    crash recovery — re-resolving on next spawn must not break."""
    repo = _init_repo(tmp_path)
    p1 = await worktree.add_worktree(repo, "wip")
    p2 = await worktree.add_worktree(repo, "wip")
    assert p1 == p2
    assert p1.exists()


async def test_remove_worktree_cleans_disk_and_git_metadata(
    tmp_path: Path,
) -> None:
    repo = _init_repo(tmp_path)
    target = await worktree.add_worktree(repo, "to-remove")
    assert target.exists()
    await worktree.remove_worktree(target)
    assert not target.exists()
    # Git's worktree list shouldn't show it anymore.
    out = subprocess.check_output(
        ["git", "-C", str(repo), "worktree", "list", "--porcelain"],
        text=True,
    )
    assert str(target.resolve()) not in out


async def test_remove_worktree_falls_back_when_git_fails(tmp_path: Path) -> None:
    """If we manually corrupt the worktree (e.g., uncommitted changes
    git refuses to remove), the fallback rm -rf should still clean
    the directory so disk doesn't leak."""
    repo = _init_repo(tmp_path)
    target = await worktree.add_worktree(repo, "dirty")
    # Make the worktree dirty so plain ``git worktree remove`` fails.
    (target / "f").write_text("uncommitted")
    # ``--force`` actually handles dirty trees, so we have to stage
    # the failure differently: nuke .git inside the worktree to make
    # git's bookkeeping confused.
    git_link = target / ".git"
    if git_link.is_file():
        git_link.unlink()
    await worktree.remove_worktree(target)
    # Either path should leave the directory gone (the fallback rm -rf
    # cleans up even when git itself errors).
    assert not target.exists()


async def test_worktree_exists_is_false_for_unrelated_dir(tmp_path: Path) -> None:
    plain = tmp_path / "plain"
    plain.mkdir()
    assert await worktree.worktree_exists(plain) is False
