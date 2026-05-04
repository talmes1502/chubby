"""Unit tests for _resume_is_safe — the gate that decides whether
``claude --resume <id>`` is expected to succeed before the wrapper's
auto-respawn loop passes ``--resume`` on relaunch.

This is the root fix for the "empty session didn't restart" bug: when
the user Ctrl+C's a session that never had a user turn, claude's
JSONL is missing or has no user records. ``--resume`` would spin
"No conversation found" until the crash-loop guard tripped. The gate
makes the wrapper drop ``--resume`` in that case and relaunch fresh.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from chubby.wrapper.main import _resume_is_safe


@pytest.fixture
def fake_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Redirect Path.home() to a fresh tmp dir for each test."""
    monkeypatch.setenv("HOME", str(tmp_path))
    return tmp_path


def _write_jsonl(home: Path, project: str, sid: str, records: list[dict]) -> Path:
    """Helper to create ``~/.claude/projects/<project>/<sid>.jsonl``."""
    proj_dir = home / ".claude" / "projects" / project
    proj_dir.mkdir(parents=True, exist_ok=True)
    path = proj_dir / f"{sid}.jsonl"
    with path.open("w", encoding="utf-8") as f:
        for rec in records:
            f.write(json.dumps(rec) + "\n")
    return path


def test_returns_false_for_none_id(fake_home: Path) -> None:
    assert _resume_is_safe(None) is False


def test_returns_false_for_empty_id(fake_home: Path) -> None:
    assert _resume_is_safe("") is False


def test_returns_false_when_projects_root_missing(fake_home: Path) -> None:
    # No ~/.claude/projects at all.
    assert _resume_is_safe("dd16c175-fbe6-479d-8838-598f64ff68bc") is False


def test_returns_false_when_jsonl_missing(fake_home: Path) -> None:
    # Projects root exists but no jsonl matches.
    (fake_home / ".claude" / "projects").mkdir(parents=True)
    assert _resume_is_safe("dd16c175-fbe6-479d-8838-598f64ff68bc") is False


def test_returns_false_for_empty_jsonl(fake_home: Path) -> None:
    sid = "dd16c175-fbe6-479d-8838-598f64ff68bc"
    _write_jsonl(fake_home, "-Users-tal-m", sid, [])
    assert _resume_is_safe(sid) is False


def test_returns_false_for_summary_only(fake_home: Path) -> None:
    """A JSONL with only summary/system records — no user turn — is
    not resumable. This is the exact failure mode we saw in the
    screenshot ("No conversation found with session ID: dd16c175...")."""
    sid = "dd16c175-fbe6-479d-8838-598f64ff68bc"
    _write_jsonl(
        fake_home,
        "-Users-tal-m",
        sid,
        [
            {"type": "summary", "summary": "x"},
            {"type": "system", "content": "boot"},
        ],
    )
    assert _resume_is_safe(sid) is False


def test_returns_true_for_user_turn(fake_home: Path) -> None:
    sid = "dd16c175-fbe6-479d-8838-598f64ff68bc"
    _write_jsonl(
        fake_home,
        "-Users-tal-m",
        sid,
        [
            {"type": "summary", "summary": "x"},
            {"type": "user", "message": {"role": "user", "content": "hi"}},
        ],
    )
    assert _resume_is_safe(sid) is True


def test_skips_malformed_lines(fake_home: Path) -> None:
    """A JSONL with a corrupted line in the middle should still find
    a later valid user turn — corruption shouldn't disqualify the
    whole transcript."""
    sid = "dd16c175-fbe6-479d-8838-598f64ff68bc"
    proj = fake_home / ".claude" / "projects" / "-Users-tal-m"
    proj.mkdir(parents=True)
    p = proj / f"{sid}.jsonl"
    p.write_text(
        '{"type":"summary","summary":"x"}\n'
        "garbage not json\n"
        '{"type":"user","message":{"role":"user","content":"hi"}}\n',
        encoding="utf-8",
    )
    assert _resume_is_safe(sid) is True


def test_finds_jsonl_under_any_project_dir(fake_home: Path) -> None:
    """Claude's project-dir encoding is its own (and may evolve), so the
    glob must scan ``projects/*/<sid>.jsonl`` rather than predicting a
    specific subdir."""
    sid = "dd16c175-fbe6-479d-8838-598f64ff68bc"
    _write_jsonl(
        fake_home,
        "some-other-encoding-of-cwd",
        sid,
        [{"type": "user", "message": {"role": "user", "content": "hi"}}],
    )
    assert _resume_is_safe(sid) is True
