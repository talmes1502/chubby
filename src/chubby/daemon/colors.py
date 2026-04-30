"""16-color palette + LRU-ish allocator. Picks unused colors first, falls back to round-robin."""

from __future__ import annotations

import itertools

PALETTE: tuple[str, ...] = (
    "#5fafff",  # bright blue
    "#ff8787",  # salmon
    "#87d787",  # mint
    "#ffaf5f",  # orange
    "#d787d7",  # magenta
    "#5fd7d7",  # cyan
    "#d7d787",  # olive
    "#af87ff",  # lavender
    "#ff5faf",  # pink
    "#87afff",  # periwinkle
    "#d7af87",  # tan
    "#87d7af",  # seafoam
    "#ff5f5f",  # coral (avoid pure red)
    "#5fd75f",  # lime (avoid pure green)
    "#d7d7d7",  # light grey
    "#ffffaf",  # cream
)


class ColorAllocator:
    """Stateless picker. State is the caller's `in_use` set."""

    def __init__(self) -> None:
        self._round_robin = itertools.cycle(PALETTE)

    def allocate(self, *, in_use: set[str], preferred_for_name: str | None) -> str:
        if preferred_for_name and preferred_for_name in PALETTE and preferred_for_name not in in_use:
            return preferred_for_name
        for c in PALETTE:
            if c not in in_use:
                return c
        return next(self._round_robin)
