"""Tests for ANSI color formatting helpers."""

from __future__ import annotations

from chubby.cli.format import hex_to_ansi24, prefix


def test_hex_to_ansi24() -> None:
    assert hex_to_ansi24("#ff8000") == "\x1b[38;2;255;128;0m"


def test_prefix_wraps_with_color_and_reset() -> None:
    out = prefix("frontend", "#ff8000")
    assert "frontend" in out
    assert out.endswith("\x1b[0m")
