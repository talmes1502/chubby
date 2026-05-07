"""Update-availability check for ``chubby start`` and ``chubby update``.

Pulls the latest release tag from GitHub's releases/latest API,
compares it to the currently-installed ``__version__``, and (when
called from ``chubby start``) auto-installs the new version then
re-execs the originating command so the user lands inside the
newer chubby with no extra typing. Cached for 24h on disk so we
don't hit GitHub on every start.

Side-effect-free for the read paths (``latest_release_tag`` etc).
``run_update_now`` and ``auto_update_and_relaunch`` invoke
install.sh — the former exec's bash and never returns; the latter
runs install.sh as a subprocess and then re-execs chubby with the
caller's argv so the start flow continues seamlessly.

Failure modes (no network, GitHub down, malformed JSON, install.sh
exit) all degrade silently — a broken update check must never
block ``chubby start``.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path
from typing import Any

import typer
from packaging.version import InvalidVersion, Version

from chubby import __version__
from chubby.daemon import paths

# Public API:
#   - latest_release_tag()  → "0.1.2" or None
#   - is_newer(remote, local) → bool
#   - prompt_to_update_if_available()  → maybe runs install.sh
#   - run_update_now()  → unconditional reinstall
#
# Everything is plain-functions — no class — to keep the surface
# small enough that test_main.py can stub _fetch_latest_release_json
# without touching globals.

_RELEASE_API = "https://api.github.com/repos/talmes1502/chubby/releases/latest"
_INSTALL_URL = "https://raw.githubusercontent.com/talmes1502/chubby/main/install.sh"
_CACHE_TTL_S = 24 * 60 * 60  # one day


def _cache_path() -> Path:
    return paths.hub_home() / "update-check.json"


def _now() -> float:
    return time.time()


def _read_cache() -> dict[str, Any] | None:
    p = _cache_path()
    if not p.is_file():
        return None
    try:
        data = json.loads(p.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    if not isinstance(data, dict):
        return None
    if _now() - float(data.get("fetched_at", 0)) > _CACHE_TTL_S:
        return None
    return data


def _write_cache(tag: str | None) -> None:
    p = _cache_path()
    try:
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(
            json.dumps({"tag": tag, "fetched_at": _now()}),
            encoding="utf-8",
        )
    except OSError:
        # Cache write failures are non-fatal — worst case we re-fetch
        # on the next start. Don't surface to the user.
        pass


def _fetch_latest_release_json(timeout: float = 2.0) -> dict[str, Any] | None:
    """One short, hard-deadlined GET. Anything that goes wrong returns
    ``None`` — we never want a flaky network to interrupt a user
    starting chubby."""
    try:
        req = urllib.request.Request(
            _RELEASE_API,
            headers={"Accept": "application/vnd.github+json"},
        )
        with urllib.request.urlopen(req, timeout=timeout) as r:
            body = r.read()
    except Exception:
        return None
    try:
        data = json.loads(body)
    except json.JSONDecodeError:
        return None
    return data if isinstance(data, dict) else None


def latest_release_tag(*, use_cache: bool = True) -> str | None:
    """Return the latest published release's version (no leading 'v')
    or ``None`` if it can't be determined."""
    if use_cache:
        cached = _read_cache()
        if cached is not None:
            cached_tag = cached.get("tag")
            return cached_tag if isinstance(cached_tag, str) and cached_tag else None
    data = _fetch_latest_release_json()
    fetched_tag: str | None = None
    if data:
        raw = data.get("tag_name")
        if isinstance(raw, str) and raw.startswith("v"):
            fetched_tag = raw[1:]
        elif isinstance(raw, str):
            fetched_tag = raw
    _write_cache(fetched_tag)
    return fetched_tag


def is_newer(remote: str, local: str) -> bool:
    """``remote > local`` per PEP 440. Delegates to ``packaging`` —
    the same library pip / pipx / setuptools use to order releases —
    so dev / rc / post / epoch / local-segment all behave correctly
    (e.g. ``0.1.2.dev0 < 0.1.2``, prompting a dev-build user to
    install the matching tagged release). Returns ``False`` on any
    parse failure rather than raising, so a malformed tag from
    GitHub never blocks ``chubby start``."""
    try:
        return Version(remote) > Version(local)
    except InvalidVersion:
        return False


def auto_update_and_relaunch() -> None:
    """``chubby start`` calls this BEFORE bringing the daemon up.

    If a newer release exists, run install.sh inline (subprocess,
    not exec — we want to come back), and on success re-exec the
    user's chubby command with their original argv so the start
    flow continues inside the new version. The user types `chubby
    start` once, sees ``upgrading...`` for ~30 s, then the TUI
    opens — no second invocation needed.

    Skipped (returns silently) when:
      * ``CHUBBY_NO_AUTO_UPDATE=1`` is in the env (escape hatch);
      * stdin is not a TTY (pipx-run / CI: don't surprise scripts
        with an unsolicited 30-second download);
      * GitHub is unreachable / cache says no newer version;
      * install.sh exits non-zero (we keep the old version
        running and surface the failure).

    Bypasses the 24h cache so a release tagged 30 s ago is picked
    up instead of being shadowed by yesterday's "no update" cache
    entry.
    """
    if os.environ.get("CHUBBY_NO_AUTO_UPDATE"):
        return
    if not sys.stdin.isatty():
        # Non-interactive: don't autonomously download a new binary
        # over a script's connection. Tell them once.
        tag = latest_release_tag()
        if tag and is_newer(tag, __version__):
            typer.echo(
                f"chubby v{tag} is available (you have v{__version__}). "
                f"Run 'chubby update' to upgrade."
            )
        return
    tag = latest_release_tag(use_cache=False)
    if not tag or not is_newer(tag, __version__):
        return
    typer.echo(f"chubby v{tag} is available — auto-updating from v{__version__}...")
    proc = subprocess.run(
        ["bash", "-c", f'curl -fsSL "{_INSTALL_URL}" | bash'],
        env={**os.environ, "CHUBBY_FORCE": "1"},
    )
    if proc.returncode != 0:
        typer.echo(
            f"  ✗ auto-update failed (exit {proc.returncode}); "
            f"continuing with v{__version__}",
            err=True,
        )
        return
    typer.echo(f"✓ updated to v{tag} — relaunching chubby...")
    # Re-exec the new chubby binary with the user's original argv
    # (everything past argv[0]). install.sh just refreshed the
    # ~/.local/bin/chubby symlink to the new pipx venv, so PATH
    # lookup gets the upgraded entry-point.
    chubby_bin = shutil.which("chubby") or sys.argv[0]
    os.execvp(chubby_bin, [chubby_bin, *sys.argv[1:]])


def run_update_now() -> None:
    """Re-run install.sh in the foreground. Replaces the current
    process so the user sees the installer's output, then `chubby`
    is the new version when their shell returns control."""
    typer.echo(f"upgrading via {_INSTALL_URL}")
    # `bash -c "$(curl -fsSL <url>)"` is the shape install.sh expects.
    # We exec bash so the installer's own stdout streams directly
    # without our process buffering it. CHUBBY_FORCE=1 wipes the
    # current pipx venv + binary first, guaranteeing a clean upgrade.
    env = {**os.environ, "CHUBBY_FORCE": "1"}
    cmd = [
        "bash",
        "-c",
        f'curl -fsSL "{_INSTALL_URL}" | bash',
    ]
    # execvpe: replace current process. After this the shell that
    # spawned `chubby start` is talking to bash directly.
    os.execvpe("bash", cmd, env)
