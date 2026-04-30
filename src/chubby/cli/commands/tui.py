"""`chubby tui` — auto-download chubby-tui binary and exec it.

For local development the command first looks for a hand-built binary at
``~/Documents/a/Code/chubby/tui/chubby-tui`` (the path produced by
``cd tui && go build ./cmd/chubby-tui``). Falls back to the legacy
``~/Documents/a/Code/chub/tui/chubby-tui`` path so the on-disk repo
directory does not have to be renamed during the chub->chubby transition.
Otherwise it downloads the release binary for the current OS/arch from
GitHub and caches it under
``~/.cache/chubby/tui/chubby-tui-<version>``.
"""

from __future__ import annotations

import os
import platform
import sys
import urllib.request
from pathlib import Path

import typer

from chubby import __version__

CACHE = Path.home() / ".cache" / "chubby" / "tui"
# Local-dev fallbacks in priority order. The "chubby" repo dir is the
# canonical location; the "chub" repo dir is kept as a fallback so users
# who haven't renamed their on-disk checkout still get the local binary.
LOCAL_DEV_BINS = (
    Path.home() / "Documents" / "a" / "Code" / "chubby" / "tui" / "chubby-tui",
    Path.home() / "Documents" / "a" / "Code" / "chub" / "tui" / "chubby-tui",
)


def _binary_url(version: str) -> str:
    sysname = platform.system().lower()  # darwin | linux
    arch = platform.machine().lower()  # arm64 | x86_64
    arch_go = "amd64" if arch == "x86_64" else arch
    return f"https://github.com/USER/chubby/releases/download/v{version}/chubby-tui-{sysname}-{arch_go}"


def _local_dev_bin() -> Path | None:
    for p in LOCAL_DEV_BINS:
        if p.exists():
            return p
    return None


def run(
    force_download: bool = typer.Option(
        False, "--force-download", help="redownload the binary even if cached"
    ),
) -> None:
    bin_path = CACHE / f"chubby-tui-{__version__}"
    local_dev = _local_dev_bin()
    if not force_download and not bin_path.exists() and local_dev is not None:
        os.execv(str(local_dev), [str(local_dev), *sys.argv[2:]])
        return
    if force_download or not bin_path.exists():
        CACHE.mkdir(parents=True, exist_ok=True)
        url = _binary_url(__version__)
        typer.echo(f"downloading {url}")
        try:
            urllib.request.urlretrieve(url, bin_path)
        except Exception as e:
            raise typer.BadParameter(
                f"failed to download chubby-tui: {e}\n"
                f"either build it yourself (cd tui && go build ./cmd/chubby-tui) "
                f"and place it at {bin_path}, or `brew install USER/chubby/chubby-tui`."
            ) from e
        bin_path.chmod(0o755)
    os.execv(str(bin_path), [str(bin_path), *sys.argv[2:]])
