"""Helpers for reading Claude Code hook input.

Claude Code passes hook event data to user-defined commands as a JSON
object on **stdin**, not through environment variables. The shape we
care about looks like::

    {
      "session_id": "<uuid>",
      "transcript_path": "/Users/.../<session_id>.jsonl",
      "cwd": "/path/to/project",
      ...
    }

(The exact keys vary per event, but ``session_id`` and ``cwd`` are
always present for the events chubby cares about.)

Pre-stdin-aware versions of chubby's hook commands accepted ``--claude-
session-id $CLAUDE_SESSION_ID`` on the command line. That env var is
not set by Claude Code, so the substitution evaluated to empty and
the typer parser refused the call — every Stop hook fired silently
into the void, ``mark_idle`` never reached the daemon, and the
``AWAITING_USER`` redraw chubby relies on never triggered. These
helpers let the commands fall back to the (correct) stdin payload
whenever the corresponding flag is missing.
"""

from __future__ import annotations

import json
import sys
from typing import Any


def read_hook_payload() -> dict[str, Any]:
    """Read Claude Code's hook event JSON from stdin. Returns an empty
    dict if stdin is closed, empty, or not valid JSON — callers fall
    back to whichever flags they were passed (or no-op).

    Non-blocking-ish: if stdin is a TTY (i.e., the user invoked the
    command directly rather than via a hook), we don't hang waiting
    for input. Hooks always pipe stdin, so this is safe.
    """
    if sys.stdin.isatty():
        return {}
    try:
        raw = sys.stdin.read()
    except (OSError, ValueError):
        return {}
    if not raw.strip():
        return {}
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        return {}
    return data if isinstance(data, dict) else {}
