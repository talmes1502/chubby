"""`chub tui` — auto-download chub-tui binary and exec it.

For local development the command first looks for a hand-built binary at
``~/Documents/a/Code/chub/tui/chub-tui`` (the path produced by
``cd tui && go build ./cmd/chub-tui``). Otherwise it downloads the
release binary for the current OS/arch from GitHub and caches it under
``~/.cache/chub/tui/chub-tui-<version>``.
"""

from __future__ import annotations

import os
import platform
import sys
import urllib.request
from pathlib import Path

import typer

from chub import __version__

CACHE = Path.home() / ".cache" / "chub" / "tui"
LOCAL_DEV_BIN = Path.home() / "Documents" / "a" / "Code" / "chub" / "tui" / "chub-tui"


def _binary_url(version: str) -> str:
    sysname = platform.system().lower()  # darwin | linux
    arch = platform.machine().lower()  # arm64 | x86_64
    arch_go = "amd64" if arch == "x86_64" else arch
    return f"https://github.com/USER/chub/releases/download/v{version}/chub-tui-{sysname}-{arch_go}"


def run(
    force_download: bool = typer.Option(
        False, "--force-download", help="redownload the binary even if cached"
    ),
) -> None:
    bin_path = CACHE / f"chub-tui-{__version__}"
    if not force_download and not bin_path.exists() and LOCAL_DEV_BIN.exists():
        os.execv(str(LOCAL_DEV_BIN), [str(LOCAL_DEV_BIN), *sys.argv[2:]])
        return
    if force_download or not bin_path.exists():
        CACHE.mkdir(parents=True, exist_ok=True)
        url = _binary_url(__version__)
        typer.echo(f"downloading {url}")
        try:
            urllib.request.urlretrieve(url, bin_path)
        except Exception as e:
            raise typer.BadParameter(
                f"failed to download chub-tui: {e}\n"
                f"either build it yourself (cd tui && go build ./cmd/chub-tui) "
                f"and place it at {bin_path}, or `brew install USER/chub/chub-tui`."
            ) from e
        bin_path.chmod(0o755)
    os.execv(str(bin_path), [str(bin_path), *sys.argv[2:]])
