"""``chubby tui`` — exec the chubby-tui Go binary.

Resolution order (first hit wins):

  1. ``chubby-tui`` on ``$PATH`` — the canonical post-install location
     (``install.sh`` drops it at ``~/.local/bin/chubby-tui``).
  2. Author's local-dev build — ``~/Documents/a/Code/chub*/tui/chubby-tui``,
     so a hand-built ``go build ./cmd/chubby-tui`` is picked up while
     iterating without re-running the installer.

If neither hits, we error with the install command rather than falling
back to a tarball download. The release-asset auto-download path was
removed: it required a tagged GitHub release to exist before any user
could run ``chubby tui``, which created a chicken-and-egg between
"first colleague tries chubby" and "first release tagged." Both
install paths produce the same Go binary; one is enough.
"""

from __future__ import annotations

import os
import shutil
import sys
from pathlib import Path

import typer

from chubby.daemon import paths

# Author's local-dev fallbacks (repo checkout). Iterated in order;
# first that exists wins. Keeping both spellings here so a user mid-
# rename of the on-disk repo dir still gets the local binary.
LOCAL_DEV_BINS = (
    Path.home() / "Documents" / "a" / "Code" / "chubby" / "tui" / "chubby-tui",
    Path.home() / "Documents" / "a" / "Code" / "chub" / "tui" / "chubby-tui",
)

INSTALL_HINT = (
    "chubby-tui binary not found. Install with:\n"
    "  curl -fsSL https://raw.githubusercontent.com/talmes1502/chubby/main/install.sh | bash\n"
    "or build manually:\n"
    "  go install github.com/talmes1502/chubby/tui/cmd/chubby-tui@latest"
)


def _resolve_binary() -> Path | None:
    """Return the path to a usable chubby-tui binary, or None.

    Honors CHUBBY_TUI_BIN as an explicit override (escape hatch for
    test machines / CI / users who keep the binary at a non-standard
    location). Otherwise: $PATH, then the author's dev paths.
    """
    if override := os.environ.get("CHUBBY_TUI_BIN"):
        p = Path(override)
        if p.exists():
            return p
    if found := shutil.which("chubby-tui"):
        return Path(found)
    for p in LOCAL_DEV_BINS:
        if p.exists():
            return p
    return None


def _build_env() -> dict[str, str]:
    """Inject the canonical socket path so the Go binary can't disagree
    with the Python daemon about where the socket lives. Belt-and-suspenders:
    also pass CHUBBY_HOME so any leftover ``CHUB_HOME`` in the environment
    can't accidentally route the TUI to a stale legacy socket directory."""
    env = os.environ.copy()
    env["CHUBBY_SOCK"] = str(paths.sock_path())
    env["CHUBBY_HOME"] = str(paths.hub_home())
    # Drop the legacy fallback so the Go binary's chubbyEnv() doesn't
    # latch onto a CHUB_HOME from the user's shell that points at a
    # different (possibly stale) directory than what the Python side
    # is actually using.
    env.pop("CHUB_HOME", None)
    env.pop("CHUB_SOCK", None)
    return env


def run(
    focus: str | None = typer.Option(None, "--focus", help="Pre-focus this session at startup"),
    detached: bool = typer.Option(
        False, "--detached", help="Start with rail collapsed (compact view)"
    ),
) -> None:
    bin_path = _resolve_binary()
    if bin_path is None:
        raise typer.BadParameter(INSTALL_HINT)

    env = _build_env()
    # The Go binary doesn't parse flags itself — these typer options
    # are forwarded to it via env vars (same channel as CHUBBY_SOCK).
    # The flags still pass through sys.argv unchanged because the Go
    # binary simply ignores extra argv entries.
    if focus:
        env["CHUBBY_FOCUS_SESSION"] = focus
    if detached:
        env["CHUBBY_DETACHED"] = "1"
    os.execvpe(str(bin_path), [str(bin_path), *sys.argv[2:]], env)
