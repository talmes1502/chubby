import time

from chub.daemon import clock


def test_now_returns_monotonic_increasing_int_ms() -> None:
    a = clock.now_ms()
    time.sleep(0.005)
    b = clock.now_ms()
    assert isinstance(a, int)
    assert b > a
    assert b - a >= 5


def test_freeze_overrides_now() -> None:
    with clock.freeze(1_700_000_000_000):
        assert clock.now_ms() == 1_700_000_000_000
        assert clock.now_ms() == 1_700_000_000_000
