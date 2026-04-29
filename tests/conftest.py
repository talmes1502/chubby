"""Shared fixtures: tempdir CHUB_HOME, fakeclaude on PATH."""

from __future__ import annotations

import os
import shutil
import sys
import tempfile
from collections.abc import Iterator
from pathlib import Path

import pytest


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
    """A short-prefix CHUB_HOME so AF_UNIX sun_path stays under macOS limits."""
    short_dir = Path(tempfile.mkdtemp(prefix="chub-"))
    monkeypatch.setenv("CHUB_HOME", str(short_dir))
    try:
        yield short_dir
    finally:
        shutil.rmtree(short_dir, ignore_errors=True)
