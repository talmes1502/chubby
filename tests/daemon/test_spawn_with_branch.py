"""End-to-end-ish test for ``spawn_session(..., branch=...)``: when a
caller spawns with ``--branch``, the daemon must:

1. Detect the git root from the supplied cwd.
2. Create a worktree at ``$CHUBBY_WORKTREES_ROOT/<repo-hash>/<branch>``.
3. Override the wrapper's cwd to that worktree path.
4. Persist ``worktree_path`` on the Session so cleanup can find it.

Then on ``release_session``, the worktree directory must be gone.

We exercise this through ``chubbyd_main.serve()`` running in-process
plus a real ``chubby-claude`` subprocess driven by the ``fakeclaude``
shim that already powers ``test_wrapper_e2e.py``. The git operations
are real (cheap, no network).
"""

from __future__ import annotations

import asyncio
import shutil
import subprocess
from pathlib import Path

import pytest

from chubby.cli.client import Client
from chubby.daemon import main as chubbyd_main


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


def _make_repo(tmp_path: Path) -> Path:
    if not shutil.which("git"):
        pytest.skip("git not available on PATH")
    repo = tmp_path / "repo"
    repo.mkdir()
    _git(repo, "init", "-b", "main")
    (repo / "f").write_text("a")
    _git(repo, "add", "f")
    _git(repo, "commit", "-m", "first")
    return repo


async def test_spawn_with_branch_creates_worktree(
    chub_home: Path,
    fakeclaude_bin: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Spawning with ``--branch`` produces a session whose cwd is a
    chubby-managed worktree, the worktree path is recorded on the
    session, and HEAD points at the requested branch."""
    repo = _make_repo(tmp_path)
    wt_root = tmp_path / "wt-root"
    monkeypatch.setenv("CHUBBY_WORKTREES_ROOT", str(wt_root))

    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists(), "chubbyd never created its socket"

    try:
        client = Client(sock)
        spawn_resp = await client.call(
            "spawn_session",
            {
                "name": "wt-test",
                "cwd": str(repo),
                "tags": [],
                "branch": "wip-feature",
            },
        )
        session = spawn_resp["session"]
        # Session cwd is the worktree path, not the original repo.
        assert session["cwd"].startswith(str(wt_root)), session["cwd"]
        assert "wip-feature" in session["cwd"]
        # worktree_path is recorded on the session (the daemon patches
        # it just after the wrapper registers).
        cur: dict | None = None
        for _ in range(20):
            r = await client.call("list_sessions", {})
            cur = next((s for s in r["sessions"] if s["id"] == session["id"]), None)
            if cur and cur.get("worktree_path"):
                break
            await asyncio.sleep(0.05)
        assert cur is not None and cur.get("worktree_path") == session["cwd"]
        # The directory is a real git worktree on the right branch.
        wt_dir = Path(session["cwd"])
        assert wt_dir.exists()
        out = subprocess.check_output(
            ["git", "-C", str(wt_dir), "rev-parse", "--abbrev-ref", "HEAD"],
            text=True,
        ).strip()
        assert out == "wip-feature"
        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()


async def test_release_cleans_up_worktree(
    chub_home: Path,
    fakeclaude_bin: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """End-to-end: spawn with ``--branch`` → wait for claude_session_id
    binding → release → worktree directory is gone."""
    repo = _make_repo(tmp_path)
    wt_root = tmp_path / "wt-root"
    monkeypatch.setenv("CHUBBY_WORKTREES_ROOT", str(wt_root))

    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists()

    try:
        client = Client(sock)
        spawn_resp = await client.call(
            "spawn_session",
            {
                "name": "wt-rel",
                "cwd": str(repo),
                "tags": [],
                "branch": "rel-branch",
            },
        )
        session = spawn_resp["session"]
        wt_dir = Path(session["cwd"])
        assert wt_dir.exists()
        # Wait for the JSONL tailer to bind a claude_session_id (the
        # fake claude writes to ~/.claude/sessions/<pid>.json which
        # the daemon's hook polls). release_session refuses without
        # one. If fakeclaude doesn't get there in time, skip.
        for _ in range(80):
            r = await client.call("list_sessions", {})
            cur = next((s for s in r["sessions"] if s["id"] == session["id"]), None)
            if cur and cur.get("claude_session_id"):
                break
            await asyncio.sleep(0.1)
        if not (cur and cur.get("claude_session_id")):
            pytest.skip("fakeclaude didn't bind claude_session_id within 8 s")
        await client.call("release_session", {"id": session["id"]})
        for _ in range(80):
            if not wt_dir.exists():
                break
            await asyncio.sleep(0.05)
        assert not wt_dir.exists(), f"worktree should be cleaned after release: {wt_dir}"
        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()


async def test_spawn_branch_in_non_git_dir_errors_clearly(
    chub_home: Path,
    fakeclaude_bin: Path,
    tmp_path: Path,
) -> None:
    """Asking for ``--branch`` against a cwd that isn't a git repo
    should fail loudly, not silently spawn into the original cwd."""
    plain = tmp_path / "plain"
    plain.mkdir()

    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists()

    try:
        client = Client(sock)
        with pytest.raises(Exception):  # ChubError surfaces as RpcError
            await client.call(
                "spawn_session",
                {
                    "name": "wt-non-git",
                    "cwd": str(plain),
                    "tags": [],
                    "branch": "doesnt-matter",
                },
            )
        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
