from chub.daemon.colors import PALETTE, ColorAllocator


def test_palette_has_16_unique_hex_colors() -> None:
    assert len(PALETTE) == 16
    assert len(set(PALETTE)) == 16
    for c in PALETTE:
        assert c.startswith("#") and len(c) == 7


def test_allocator_picks_first_color_when_empty() -> None:
    alloc = ColorAllocator()
    color = alloc.allocate(in_use=set(), preferred_for_name=None)
    assert color == PALETTE[0]


def test_allocator_avoids_in_use_colors() -> None:
    alloc = ColorAllocator()
    color = alloc.allocate(in_use={PALETTE[0], PALETTE[1]}, preferred_for_name=None)
    assert color == PALETTE[2]


def test_allocator_uses_preferred_if_not_in_use() -> None:
    alloc = ColorAllocator()
    color = alloc.allocate(in_use=set(), preferred_for_name=PALETTE[5])
    assert color == PALETTE[5]


def test_allocator_falls_back_when_preferred_in_use() -> None:
    alloc = ColorAllocator()
    color = alloc.allocate(in_use={PALETTE[5]}, preferred_for_name=PALETTE[5])
    assert color == PALETTE[0]


def test_allocator_round_robin_when_all_taken() -> None:
    alloc = ColorAllocator()
    in_use = set(PALETTE)
    color = alloc.allocate(in_use=in_use, preferred_for_name=None)
    assert color in PALETTE
