"""``chubby update`` — pull the latest chubby + chubby-tui.

Wraps ``install.sh`` with ``CHUBBY_FORCE=1`` so a stale pipx venv
or out-of-date binary gets replaced cleanly.
"""

from __future__ import annotations

import typer

from chubby import __version__
from chubby.cli.commands import _update_check


def run() -> None:
    """Re-run install.sh against the latest GitHub release, replacing
    both the Python CLI (pipx venv) and the chubby-tui binary."""
    tag = _update_check.latest_release_tag(use_cache=False)
    if tag and not _update_check.is_newer(tag, __version__):
        typer.echo(
            f"chubby is already at v{__version__} (latest released: v{tag}). Nothing to update."
        )
        return
    if tag:
        typer.echo(f"upgrading from v{__version__} → v{tag}")
    else:
        typer.echo(
            f"could not resolve latest release tag; reinstalling from main "
            f"(current: v{__version__})"
        )
    _update_check.run_update_now()
