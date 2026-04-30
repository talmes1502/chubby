"""SessionStart-driven readonly registration + JSONL tailing.

The JSONL tailer reads Claude's per-session transcript file (one JSON
record per line) and emits two side-effects per turn:

1. Indexes the message text into FTS via ``Database.insert_message`` so
   the user can grep transcripts.
2. Broadcasts a ``transcript_message`` event so the TUI can render the
   structured conversation in real time.

Records with ``type == "user"`` or ``type == "assistant"`` are turns;
everything else is skipped. Tool-use / tool-result blocks are rendered
as one-line summaries since the TUI doesn't yet have a richer renderer
for them.
"""

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
    ``~/.claude/projects/<encoded>/<session_id>.jsonl``. Leading dashes are
    stripped (e.g. an absolute path ``/Users/me`` becomes ``Users-me``).
    """
    encoded = cwd.replace("/", "-").lstrip("-")
    return Path.home() / ".claude" / "projects" / encoded / f"{claude_session_id}.jsonl"


def _stringify(content: Any) -> str:
    """Generic stringifier used as a fallback for unstructured records."""
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


def _summarize_tool_use(block: dict[str, Any]) -> str:
    name = block.get("name", "?")
    inp = block.get("input")
    args_summary = ""
    if isinstance(inp, dict):
        # Pick the most informative-looking arg if any.
        for key in ("file_path", "path", "command", "query", "url", "pattern"):
            if isinstance(inp.get(key), str):
                args_summary = inp[key]
                break
        if not args_summary and inp:
            # Fall back to first key=value pair, truncated.
            k, v = next(iter(inp.items()))
            args_summary = f"{k}={v}"
        if len(args_summary) > 80:
            args_summary = args_summary[:77] + "..."
    return f"[tool_use: {name}({args_summary})]"


def _summarize_tool_result(block: dict[str, Any]) -> str:
    content = block.get("content", "")
    if isinstance(content, list):
        # Tool result content can itself be a list of text blocks.
        content = "".join(
            c.get("text", "") if isinstance(c, dict) else str(c) for c in content
        )
    if not isinstance(content, str):
        content = str(content)
    return f"[tool_result: {len(content)} chars]"


def _extract_turn_text(message: Any) -> str:
    """Pull the conversation text out of a Claude transcript ``message``.

    ``message.content`` is either a plain string or a list of content
    blocks (``text``, ``tool_use``, ``tool_result``). For the TUI we
    concatenate text blocks verbatim and render tool blocks as one-line
    summaries — that matches the spec's "compact" requirement without
    losing the fact that a tool was invoked.
    """
    if not isinstance(message, dict):
        return ""
    content = message.get("content")
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""
    parts: list[str] = []
    for block in content:
        if not isinstance(block, dict):
            continue
        btype = block.get("type")
        if btype == "text":
            t = block.get("text")
            if isinstance(t, str):
                parts.append(t)
        elif btype == "tool_use":
            parts.append(_summarize_tool_use(block))
        elif btype == "tool_result":
            parts.append(_summarize_tool_result(block))
        # Unknown block types are silently dropped — they're rare and
        # not user-facing conversation.
    return "\n".join(parts)


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
    """Tail ``path`` line-by-line, dispatching turns to FTS and the event bus.

    Records are filtered to ``type in {"user", "assistant"}``; their text
    content is extracted, indexed into FTS, and broadcast as a
    ``transcript_message`` event so live TUI clients can render the
    conversation.

    ``stop_after`` is for tests — it returns after that many turns have
    been processed.
    """
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
            rtype = rec.get("type")
            if rtype not in ("user", "assistant"):
                continue
            message = rec.get("message")
            role = "user" if rtype == "user" else "assistant"
            text = _extract_turn_text(message)
            if not text:
                # Skip empty turns (e.g. assistant message containing only
                # a tool_use we already summarised to "" — shouldn't happen,
                # but be safe).
                continue
            ts = now_ms()
            if registry.db is not None:
                await registry.db.insert_message(
                    session_id=session_id,
                    hub_run_id=registry.hub_run_id,
                    ts=ts,
                    role=role,
                    content=text,
                )
            if registry.subs is not None:
                await registry.subs.broadcast(
                    "transcript_message",
                    {
                        "session_id": session_id,
                        "role": role,
                        "text": text,
                        "ts": ts,
                    },
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
