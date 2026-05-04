"""Shared fixtures: tempdir CHUBBY_HOME, fakeclaude on PATH, suppressed
desktop notifications.
"""

from __future__ import annotations

import os
import shutil
import sys
import tempfile
from collections.abc import Iterator
from pathlib import Path

import pytest


@pytest.fixture(autouse=True)
def _suppress_desktop_notifications(monkeypatch: pytest.MonkeyPatch) -> None:
    """Stub out chubby.daemon.notify.notify for every test so nothing
    fires a real macOS / notify-send notification mid-suite. Without
    this, any test that flips a session to AWAITING_USER pops a
    desktop banner with the test session's name (e.g.
    ``chubby: test_register_readonly_then_inject``). Tests that
    specifically want to assert on notify behavior re-stub it
    locally; the autouse default is "no-op"."""

    async def _noop(title: str, body: str) -> None:
        return None

    # Patch the module attribute. Registry's import is lazy
    # (``from chubby.daemon.notify import notify`` inside the method),
    # so the patched attribute wins. Tests that import ``notify``
    # directly at module top (like ``test_notify.py``) hold a local
    # binding that bypasses this patch — they're testing the real
    # function and are responsible for stubbing subprocess themselves.
    from chubby.daemon import notify as notify_mod

    monkeypatch.setattr(notify_mod, "notify", _noop)


@pytest.fixture
def fakeclaude_bin(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Create a 'claude' shim on PATH that runs our fakeclaude.py."""
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    claude = bin_dir / "claude"
    fakeclaude_py = Path(__file__).parent / "fakeclaude" / "fakeclaude.py"
    claude.write_text(
        f"#!{sys.executable}\n"
        f"import runpy; runpy.run_path({str(fakeclaude_py)!r}, run_name='__main__')\n"
    )
    claude.chmod(0o755)
    existing_path = os.environ.get("PATH", "")
    monkeypatch.setenv("PATH", f"{bin_dir}:{existing_path}")
    return claude


@pytest.fixture
def chub_home(monkeypatch: pytest.MonkeyPatch) -> Iterator[Path]:
    """A short-prefix CHUBBY_HOME so AF_UNIX sun_path stays under macOS limits."""
    short_dir = Path(tempfile.mkdtemp(prefix="chubby-"))
    monkeypatch.setenv("CHUBBY_HOME", str(short_dir))
    try:
        yield short_dir
    finally:
        shutil.rmtree(short_dir, ignore_errors=True)
