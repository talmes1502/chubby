"""SessionStart-driven readonly registration + JSONL tailing."""

from __future__ import annotations

import asyncio
import json
import logging
from pathlib import Path
from typing import Any

from chub.daemon.clock import now_ms
from chub.daemon.registry import Registry
from chub.daemon.session import Session

log = logging.getLogger(__name__)


def claude_transcript_path(claude_session_id: str, cwd: str) -> Path:
    """Return the path to Claude's JSONL transcript for a given session.

    Claude encodes the cwd as ``cwd.replace("/", "-")`` and stores the file at
    ``~/.claude/projects/<encoded>/<session_id>.jsonl``.
    """
    encoded = cwd.replace("/", "-")
    return Path.home() / ".claude" / "projects" / encoded / f"{claude_session_id}.jsonl"


def _stringify(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return " ".join(_stringify(c) for c in content)
    if isinstance(content, dict):
        text = content.get("text")
        if isinstance(text, str):
            return text
        return json.dumps(content)
    if content is None:
        return ""
    return str(content)


def _read_new_lines(path: Path, pos: int) -> tuple[int, list[str]]:
    """Synchronous helper: open ``path`` from ``pos`` and return new lines + new pos."""
    if not path.exists():
        return pos, []
    with open(path, encoding="utf-8") as f:
        f.seek(pos)
        lines = [ln.rstrip("\n") for ln in f]
        new_pos = f.tell()
    return new_pos, lines


async def _tail_jsonl(
    registry: Registry,
    session_id: str,
    path: Path,
    *,
    stop_after: int | None = None,
) -> None:
    pos = 0
    indexed = 0
    while True:
        pos, lines = await asyncio.to_thread(_read_new_lines, path, pos)
        for line in lines:
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            role = rec.get("role", "system")
            if not isinstance(role, str):
                role = "system"
            content = _stringify(rec.get("content"))
            if registry.db is not None:
                await registry.db.insert_message(
                    session_id=session_id,
                    hub_run_id=registry.hub_run_id,
                    ts=now_ms(),
                    role=role,
                    content=content,
                )
            indexed += 1
            if stop_after is not None and indexed >= stop_after:
                return
        await asyncio.sleep(0.5)


async def start_tailer(registry: Registry, s: Session) -> None:
    if s.claude_session_id is None:
        return
    path = claude_transcript_path(s.claude_session_id, s.cwd)
    await _tail_jsonl(registry, s.id, path)
