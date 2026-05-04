"""Output-mode dispatch for the CLI.

Three modes: ``pretty`` (the default for an interactive human terminal),
``json`` (machine-parseable; lists print arrays, objects print dicts —
no ``{"data": ...}`` envelope), and ``quiet`` (one id per line for
arrays, the bare id for single objects). Mode is resolved once at app
startup and held in module-level state; commands import :data:`OUT`
and call ``OUT.list(...)`` / ``OUT.object(...)`` / ``OUT.id(...)``.

Auto-detection: if any of the following env vars is non-empty, the
default mode flips to JSON so an outer agent (Claude Code, Codex,
Gemini CLI, CI) gets parseable output without needing ``--json``:
``CLAUDE_CODE``, ``CLAUDECODE``, ``CLAUDE_CODE_ENTRYPOINT``,
``CODEX_CLI``, ``GEMINI_CLI``, ``CHUBBY_AGENT``, ``CI``. Explicit
``--json`` or ``--quiet`` always wins over the auto-detected default.
"""

from __future__ import annotations

import json
import os
from collections.abc import Callable
from enum import StrEnum
from typing import Any

import typer


class Mode(StrEnum):
    PRETTY = "pretty"
    JSON = "json"
    QUIET = "quiet"


# Env vars that signal "an agent is invoking us" — flip default to JSON.
# The list mirrors Superset's CLI auto-detection set so chubby is
# scriptable from inside any of the same outer-agent contexts.
_AGENT_ENV_VARS: tuple[str, ...] = (
    "CLAUDE_CODE",
    "CLAUDECODE",
    "CLAUDE_CODE_ENTRYPOINT",
    "CODEX_CLI",
    "GEMINI_CLI",
    "CHUBBY_AGENT",
    "CI",
)


def _detect_default_mode() -> Mode:
    """JSON if any agent-context env var is set, else pretty."""
    for var in _AGENT_ENV_VARS:
        if os.environ.get(var):
            return Mode.JSON
    return Mode.PRETTY


class _Output:
    """Module-level singleton. Commands import :data:`OUT` and call
    methods; the resolved mode is shared across the process.
    """

    def __init__(self) -> None:
        # Default is overwritten in configure() at every CLI
        # invocation — env vars may be set by the parent shell
        # *after* this module imports, and tests need to vary env
        # per-test. Holding PRETTY as the import-time default keeps
        # non-Typer call sites safe.
        self.mode: Mode = Mode.PRETTY

    def set_mode(self, mode: Mode) -> None:
        self.mode = mode

    def list(
        self,
        rows: list[dict[str, Any]],
        pretty_line: Callable[[dict[str, Any]], str] | None = None,
        empty_message: str = "(empty)",
    ) -> None:
        """Render a list of dicts. ``pretty_line`` formats one row in
        ``PRETTY`` mode; if omitted, falls back to ``json.dumps(row)``
        per line."""
        if self.mode is Mode.JSON:
            typer.echo(json.dumps(rows))
            return
        if self.mode is Mode.QUIET:
            for r in rows:
                rid = r.get("id")
                if isinstance(rid, str):
                    typer.echo(rid)
                else:
                    typer.echo(json.dumps(r))
            return
        # PRETTY
        if not rows:
            typer.echo(empty_message)
            return
        for r in rows:
            line = pretty_line(r) if pretty_line is not None else json.dumps(r)
            typer.echo(line)

    def object(
        self,
        obj: dict[str, Any],
        pretty_line: Callable[[dict[str, Any]], str] | None = None,
    ) -> None:
        """Render a single dict. ``pretty_line`` formats it in
        ``PRETTY`` mode; ``QUIET`` prints the ``id`` field."""
        if self.mode is Mode.JSON:
            typer.echo(json.dumps(obj))
            return
        if self.mode is Mode.QUIET:
            rid = _extract_id(obj)
            if rid is not None:
                typer.echo(rid)
            else:
                typer.echo(json.dumps(obj))
            return
        # PRETTY
        line = pretty_line(obj) if pretty_line is not None else json.dumps(obj)
        typer.echo(line)


def _extract_id(obj: dict[str, Any]) -> str | None:
    """Pick out an ``id`` from common response shapes:
    ``{"id": "..."}`` or ``{"session": {"id": "..."}}``."""
    rid = obj.get("id")
    if isinstance(rid, str):
        return rid
    sess = obj.get("session")
    if isinstance(sess, dict):
        sid = sess.get("id")
        if isinstance(sid, str):
            return sid
    return None


# Module-level singleton imported by commands.
OUT = _Output()


def configure(*, json_flag: bool, quiet_flag: bool) -> None:
    """Apply explicit ``--json`` / ``--quiet`` flags from the Typer
    callback, falling back to env-var auto-detection. ``--quiet``
    wins over ``--json`` when both are set (matching Superset's
    precedence: quiet is the more specific intent — "ids only" — so
    it dominates "structured output")."""
    if quiet_flag:
        OUT.set_mode(Mode.QUIET)
    elif json_flag:
        OUT.set_mode(Mode.JSON)
    else:
        OUT.set_mode(_detect_default_mode())
