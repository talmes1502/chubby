"""Tests for the cross-project history scanner.

Creates fake JSONLs under a tmp ``~/.claude/projects/`` and verifies
``list_all_claude_jsonls`` returns them sorted by mtime DESC with
the right shape.
"""

from __future__ import annotations

import json
import os
import time
from pathlib import Path

import pytest

from chubby.daemon import hooks


@pytest.fixture
def fake_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("HOME", str(tmp_path))
    return tmp_path


def _write_session(home: Path, project: str, sid: str, cwd: str, prompt: str) -> Path:
    proj_dir = home / ".claude" / "projects" / project
    proj_dir.mkdir(parents=True, exist_ok=True)
    path = proj_dir / f"{sid}.jsonl"
    with path.open("w", encoding="utf-8") as f:
        f.write(json.dumps({"type": "summary", "summary": "x", "cwd": cwd}) + "\n")
        f.write(
            json.dumps(
                {
                    "type": "user",
                    "message": {"role": "user", "content": prompt},
                    "cwd": cwd,
                }
            )
            + "\n"
        )
    return path


def test_returns_empty_when_no_projects_dir(fake_home: Path) -> None:
    assert hooks.list_all_claude_jsonls() == []


def test_lists_sessions_with_metadata(fake_home: Path) -> None:
    _write_session(fake_home, "-Users-foo-myrepo", "sid-a", "/Users/foo/myrepo", "explain ssm")
    _write_session(fake_home, "-Users-foo-other", "sid-b", "/Users/foo/other", "fix the bug")
    out = hooks.list_all_claude_jsonls()
    assert len(out) == 2
    by_id = {e["claude_session_id"]: e for e in out}
    assert by_id["sid-a"]["cwd"] == "/Users/foo/myrepo"
    assert by_id["sid-a"]["first_user_message"] == "explain ssm"
    assert by_id["sid-b"]["first_user_message"] == "fix the bug"
    for e in out:
        assert e["mtime_ms"] > 0
        assert e["size"] > 0


def test_sorted_by_mtime_desc(fake_home: Path) -> None:
    """Most-recently-modified session appears first — that's the
    default order users want for "what was I just working on?"."""
    older = _write_session(fake_home, "p1", "old", "/x", "old prompt")
    # Bump older's mtime explicitly, then create a newer one.
    os.utime(older, (time.time() - 60, time.time() - 60))
    _write_session(fake_home, "p2", "new", "/y", "new prompt")
    out = hooks.list_all_claude_jsonls()
    assert [e["claude_session_id"] for e in out] == ["new", "old"]


def test_limit_caps_results(fake_home: Path) -> None:
    """Users with thousands of historical sessions get the most-recent
    N. The expensive first-user-turn read only runs over the kept N."""
    for i in range(5):
        _write_session(fake_home, f"p{i}", f"sid-{i}", f"/x{i}", f"prompt {i}")
    out = hooks.list_all_claude_jsonls(limit=2)
    assert len(out) == 2


def test_first_message_truncated(fake_home: Path) -> None:
    long = "a" * 500
    _write_session(fake_home, "p1", "sid", "/x", long)
    out = hooks.list_all_claude_jsonls(max_chars_preview=120)
    assert len(out) == 1
    msg = out[0]["first_user_message"]
    assert msg is not None
    assert len(msg) == 120
    assert msg.endswith("…")


def test_no_user_turn_yields_none_preview(fake_home: Path) -> None:
    """A JSONL with only the bootstrap summary record (no prompt yet)
    appears in the list with ``first_user_message=None`` so the user
    can still see/resume it."""
    proj = fake_home / ".claude" / "projects" / "p1"
    proj.mkdir(parents=True)
    p = proj / "sid.jsonl"
    p.write_text(
        json.dumps({"type": "summary", "summary": "x", "cwd": "/x"}) + "\n",
        encoding="utf-8",
    )
    out = hooks.list_all_claude_jsonls()
    assert len(out) == 1
    assert out[0]["first_user_message"] is None
