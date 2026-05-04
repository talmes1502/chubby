"""Unit tests for RespawnGuard. The guard tracks consecutive fast
claude exits so a broken claude binary stops respawning forever; a
single long-enough run resets the counter, and the wrapper resets it
explicitly on user-initiated /refresh-claude.
"""

from __future__ import annotations

from chubby.wrapper.main import (
    _RESPAWN_MAX_FAST_EXITS,
    _RESPAWN_RESET_WINDOW_S,
    RespawnGuard,
)


def test_fresh_guard_is_not_crash_looping() -> None:
    g = RespawnGuard()
    assert g.fast_exit_count == 0
    assert not g.is_crash_looping()


def test_fast_exits_accumulate() -> None:
    g = RespawnGuard()
    for i in range(1, _RESPAWN_MAX_FAST_EXITS):
        g.record_exit(0.1)
        assert g.fast_exit_count == i
        assert not g.is_crash_looping()


def test_threshold_triggers_crash_loop() -> None:
    g = RespawnGuard()
    for _ in range(_RESPAWN_MAX_FAST_EXITS):
        g.record_exit(0.1)
    assert g.is_crash_looping()


def test_long_run_resets_then_increments() -> None:
    """A run longer than the reset window clears the prior streak. The
    long run itself still counts as a (newly-started) fast-exit chain
    of length 1."""
    g = RespawnGuard()
    for _ in range(_RESPAWN_MAX_FAST_EXITS - 1):
        g.record_exit(0.1)
    assert g.fast_exit_count == _RESPAWN_MAX_FAST_EXITS - 1
    g.record_exit(_RESPAWN_RESET_WINDOW_S + 1.0)
    assert g.fast_exit_count == 1
    assert not g.is_crash_looping()


def test_explicit_reset_clears_counter() -> None:
    """User-initiated /refresh-claude resets the consecutive-crash
    chain even though the prior runs were fast."""
    g = RespawnGuard()
    for _ in range(_RESPAWN_MAX_FAST_EXITS - 1):
        g.record_exit(0.1)
    g.reset()
    assert g.fast_exit_count == 0
    assert not g.is_crash_looping()


def test_boundary_run_at_window_counts_as_fast() -> None:
    """A run exactly equal to the reset window is treated as fast (the
    threshold is strict ``>``)."""
    g = RespawnGuard()
    g.record_exit(_RESPAWN_RESET_WINDOW_S)
    assert g.fast_exit_count == 1
