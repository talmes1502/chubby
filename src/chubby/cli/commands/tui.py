"""`chubby tui` — auto-download chubby-tui binary and exec it.

For local development the command first looks for a hand-built binary at
``~/Documents/a/Code/chubby/tui/chubby-tui`` (the path produced by
``cd tui && go build ./cmd/chubby-tui``). Falls back to the legacy
``~/Documents/a/Code/chub/tui/chubby-tui`` path so the on-disk repo
directory does not have to be renamed during the chub->chubby transition.
Otherwise it downloads the release tarball produced by GoReleaser
(see ``tui/.goreleaser.yaml``), verifies its sha256 against
``checksums.txt`` from the same release, and caches the extracted
binary under ``~/.cache/chubby/tui/chubby-tui-<version>``.
"""

from __future__ import annotations

import hashlib
import io
import os
import platform
import sys
import tarfile
import urllib.request
from pathlib import Path

import typer

from chubby import __version__
from chubby.daemon import paths

CACHE = Path.home() / ".cache" / "chubby" / "tui"
# Local-dev fallbacks in priority order. The "chubby" repo dir is the
# canonical location; the "chub" repo dir is kept as a fallback so users
# who haven't renamed their on-disk checkout still get the local binary.
LOCAL_DEV_BINS = (
    Path.home() / "Documents" / "a" / "Code" / "chubby" / "tui" / "chubby-tui",
    Path.home() / "Documents" / "a" / "Code" / "chub" / "tui" / "chubby-tui",
)
RELEASE_BASE = "https://github.com/talmes1502/chubby/releases/download"


def _archive_name(version: str) -> str:
    """Return ``chubby-tui_<version>_<os>_<arch>.tar.gz`` matching the
    archive ``name_template`` in ``tui/.goreleaser.yaml``. The version
    in the file name is the tag *without* a leading ``v`` (GoReleaser's
    ``{{ .Version }}`` strips it)."""
    sysname = platform.system().lower()  # darwin | linux
    arch = platform.machine().lower()  # arm64 | x86_64
    arch_go = "amd64" if arch == "x86_64" else arch
    return f"chubby-tui_{version}_{sysname}_{arch_go}.tar.gz"


def _release_url(version: str, asset: str) -> str:
    return f"{RELEASE_BASE}/v{version}/{asset}"


def _fetch(url: str) -> bytes:
    with urllib.request.urlopen(url, timeout=60) as r:
        return r.read()


def _expected_sha256(checksums: bytes, asset: str) -> str | None:
    """Parse a goreleaser ``checksums.txt`` (one ``<sha256>  <name>``
    per line) and return the digest for ``asset``."""
    for line in checksums.decode("utf-8", errors="replace").splitlines():
        digest, _, name = line.strip().partition("  ")
        if name == asset:
            return digest
    return None


def _download_and_extract(version: str, dest: Path) -> None:
    """Pull the matching release tarball, verify its sha256 against
    the release's ``checksums.txt``, and write the inner ``chubby-tui``
    binary to ``dest``. Raises on any mismatch — better to refuse than
    to exec an unverified binary the user might assume is genuine."""
    asset = _archive_name(version)
    archive_url = _release_url(version, asset)
    checksums_url = _release_url(version, "checksums.txt")

    typer.echo(f"downloading {archive_url}")
    archive = _fetch(archive_url)

    typer.echo(f"verifying against {checksums_url}")
    checksums = _fetch(checksums_url)
    expected = _expected_sha256(checksums, asset)
    if expected is None:
        raise RuntimeError(f"{asset} not listed in checksums.txt — release may be incomplete")
    actual = hashlib.sha256(archive).hexdigest()
    if actual != expected:
        raise RuntimeError(
            f"sha256 mismatch for {asset}: expected {expected}, got {actual}"
        )

    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as tf:
        member = tf.getmember("chubby-tui")
        extracted = tf.extractfile(member)
        if extracted is None:
            raise RuntimeError(f"chubby-tui not present inside {asset}")
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(extracted.read())
        dest.chmod(0o755)


def _local_dev_bin() -> Path | None:
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
    force_download: bool = typer.Option(
        False, "--force-download", help="redownload the binary even if cached"
    ),
    focus: str | None = typer.Option(None, "--focus", help="Pre-focus this session at startup"),
    detached: bool = typer.Option(
        False, "--detached", help="Start with rail collapsed (compact view)"
    ),
) -> None:
    bin_path = CACHE / f"chubby-tui-{__version__}"
    local_dev = _local_dev_bin()
    env = _build_env()
    # The Go binary doesn't parse flags itself — these typer options
    # are forwarded to it via env vars (same channel as CHUBBY_SOCK).
    # The flags still pass through sys.argv unchanged because the Go
    # binary simply ignores extra argv entries.
    if focus:
        env["CHUBBY_FOCUS_SESSION"] = focus
    if detached:
        env["CHUBBY_DETACHED"] = "1"
    if not force_download and not bin_path.exists() and local_dev is not None:
        os.execvpe(str(local_dev), [str(local_dev), *sys.argv[2:]], env)
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
    os.execvpe(str(bin_path), [str(bin_path), *sys.argv[2:]], env)
