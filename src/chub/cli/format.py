"""ANSI 24-bit color helpers for CLI output."""

from __future__ import annotations

RESET = "\x1b[0m"


def hex_to_ansi24(hex_color: str) -> str:
    h = hex_color.lstrip("#")
    r, g, b = int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16)
    return f"\x1b[38;2;{r};{g};{b}m"


def prefix(name: str, color: str) -> str:
    return f"{hex_to_ansi24(color)}[{name}]{RESET}"
