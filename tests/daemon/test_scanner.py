"""Tests for the process scanner used by ``chubby attach``."""

from __future__ import annotations

import os

from chubby.daemon.attach.scanner import (
    Candidate,
    _enumerate_claude_pids,
    _parent_pid,
    _process_cwd,
    _tmux_pane_pids,
    _tmux_target_for,
)


async def test_enumerate_returns_list() -> None:
    pids = await _enumerate_claude_pids()
    assert isinstance(pids, list)
    for p in pids:
        assert isinstance(p, int)


async def test_process_cwd_for_self() -> None:
    cwd = await _process_cwd(os.getpid())
    if cwd is not None:  # may be None in some sandboxed CI
        # ASYNC240 noqa: just normalising paths in the assertion, not real I/O.
        assert os.path.abspath(cwd) == os.path.abspath(os.getcwd())  # noqa: ASYNC240


async def test_process_cwd_for_nonexistent_pid() -> None:
    # very high pid number unlikely to exist
    assert await _process_cwd(999_999_999) is None


async def test_tmux_pane_pids_returns_dict() -> None:
    panes = await _tmux_pane_pids()
    assert isinstance(panes, dict)


async def test_tmux_target_for_no_match() -> None:
    # No panes mapped at all -> walk parent chain, return None
    target = await _tmux_target_for(os.getpid(), pane_pids={})
    assert target is None


async def test_tmux_target_for_direct_match() -> None:
    target = await _tmux_target_for(os.getpid(), pane_pids={os.getpid(): "s:0.0"})
    assert target == "s:0.0"


def test_parent_pid_returns_int_or_none() -> None:
    p = _parent_pid(os.getpid())
    assert p is None or isinstance(p, int)


def test_candidate_dataclass() -> None:
    c = Candidate(pid=1, cwd="/tmp", tmux_target=None, classification="promote_required")
    assert c.pid == 1
    assert c.classification == "promote_required"
