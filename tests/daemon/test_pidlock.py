import os
from pathlib import Path

import pytest

from chub.daemon.pidlock import PidLockBusy, acquire


def test_acquire_writes_pid(tmp_path: Path) -> None:
    p = tmp_path / "hub.pid"
    with acquire(p):
        assert p.read_text().strip() == str(os.getpid())
    assert not p.exists()


def test_acquire_raises_when_held_by_alive_process(tmp_path: Path) -> None:
    p = tmp_path / "hub.pid"
    with acquire(p):
        with pytest.raises(PidLockBusy) as exc:
            with acquire(p):
                pass
        assert exc.value.pid == os.getpid()


def test_acquire_steals_stale_lock(tmp_path: Path) -> None:
    # Simulate a stale PID file: pid=1 (init, will pass kill(0) but
    # we use pid=99999999 which almost certainly doesn't exist).
    p = tmp_path / "hub.pid"
    p.write_text("99999999\n")
    with acquire(p):
        assert p.read_text().strip() == str(os.getpid())
