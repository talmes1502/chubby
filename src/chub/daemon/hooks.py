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
import re
from pathlib import Path
from typing import Any

from chub.daemon.clock import now_ms
from chub.daemon.registry import Registry
from chub.daemon.session import Session

log = logging.getLogger(__name__)


_CAVEAT_RE = re.compile(r"<local-command-caveat>.*?</local-command-caveat>", re.DOTALL)
_CMD_RE = re.compile(
    r"<command-name>(?P<name>.*?)</command-name>"
    r"(?:\s*<command-message>.*?</command-message>)?"
    r"(?:\s*<command-args>(?P<args>.*?)</command-args>)?",
    re.DOTALL,
)
_STDOUT_RE = re.compile(
    r"<local-command-stdout>(?P<body>.*?)</local-command-stdout>", re.DOTALL
)


def _strip_command_xml(text: str) -> str:
    """Convert Claude's raw ``<command-*>`` XML tags into a Claude-UI-style
    rendering.

    Claude's JSONL stores user-typed slash commands (``/model``, ``/clear``,
    etc.) as a synthetic user message whose ``content`` is an XML blob like
    ``<command-name>/model</command-name><command-args>opus</command-args>``
    plus a ``<local-command-caveat>`` boilerplate prefix and an optional
    ``<local-command-stdout>`` postscript. Rendering the raw XML in our
    transcript view is noisy; this helper collapses it into the form
    Claude's own UI shows the user.
    """
    text = _CAVEAT_RE.sub("", text)

    def cmd_repl(m: re.Match[str]) -> str:
        name = m.group("name").strip()
        args = (m.group("args") or "").strip()
        if args:
            return f"{name} {args}"
        return name

    text = _CMD_RE.sub(cmd_repl, text)

    def stdout_repl(m: re.Match[str]) -> str:
        body = m.group("body").strip()
        if not body:
            return ""
        indented = "\n".join("  " + line for line in body.splitlines())
        return "\n" + indented

    text = _STDOUT_RE.sub(stdout_repl, text)

    # Collapse runs of blank lines that the substitutions can leave behind.
    text = re.sub(r"\n{3,}", "\n\n", text)
    return text.strip()


def claude_projects_root() -> Path:
    """Return ``~/.claude/projects/`` — the parent of all per-cwd subdirs."""
    return Path.home() / ".claude" / "projects"


def claude_sessions_dir() -> Path:
    """Return ``~/.claude/sessions/`` — Claude's per-pid session registry.

    Each running ``claude`` process drops a ``<pid>.json`` here whose
    ``sessionId`` field maps the pid to the session's UUID. We use this
    as a precise pid → JSONL binding instead of relying on mtime races
    when multiple Claude sessions share a cwd.
    """
    return Path.home() / ".claude" / "sessions"


def session_id_for_pid(pid: int) -> str | None:
    """Read ``~/.claude/sessions/<pid>.json`` and return the sessionId field.

    Returns ``None`` if the file is missing, unreadable, malformed, or
    lacks a string ``sessionId``. Caller is expected to retry while a
    Claude child is still booting.
    """
    p = claude_sessions_dir() / f"{pid}.json"
    if not p.is_file():
        return None
    try:
        data = json.loads(p.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    sid = data.get("sessionId")
    return sid if isinstance(sid, str) else None


def find_jsonl_for_session(claude_session_id: str) -> Path | None:
    """Locate Claude's JSONL transcript for a known session id.

    We don't know which projects/<encoded-cwd>/ subdir Claude wrote it
    in — Claude's encoding is its own and may evolve — so we just glob
    all subdirs for ``<id>.jsonl``. The id is a UUID so the match is
    unambiguous.
    """
    root = claude_projects_root()
    if not root.is_dir():
        return None
    for hit in root.glob(f"*/{claude_session_id}.jsonl"):
        return hit
    return None


def find_new_jsonl_for_cwd(cwd: str, since_ms: int) -> Path | None:
    """Find a JSONL file in ``~/.claude/projects/`` whose realpath cwd
    matches ``cwd`` and whose mtime is at or after ``since_ms``.

    We don't trust any path-encoding scheme: the JSONL itself records
    the cwd in its first record, so we open candidate files and read
    that field. Returns the most recently modified match, or None.

    This is encoding-free and works for any cwd Claude can store.
    """
    root = claude_projects_root()
    if not root.is_dir():
        return None
    target = str(Path(cwd).resolve())
    threshold_s = since_ms / 1000.0
    candidates: list[tuple[float, Path]] = []
    for jsonl in root.glob("*/*.jsonl"):
        try:
            st = jsonl.stat()
        except OSError:
            continue
        if st.st_mtime < threshold_s:
            continue
        if _jsonl_matches_cwd(jsonl, target):
            candidates.append((st.st_mtime, jsonl))
    if not candidates:
        return None
    candidates.sort(reverse=True)
    return candidates[0][1]


def _jsonl_matches_cwd(jsonl: Path, target_cwd: str) -> bool:
    """Return True if the JSONL was produced by a Claude session in ``target_cwd``.

    Reads up to the first 32 records to find a ``cwd`` field; Claude's
    early summary records carry it. If we can't determine, return False.
    """
    try:
        with open(jsonl, encoding="utf-8") as f:
            for i, line in enumerate(f):
                if i >= 32:
                    break
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    continue
                cwd = rec.get("cwd")
                if isinstance(cwd, str):
                    return str(Path(cwd).resolve()) == target_cwd
    except OSError:
        return False
    return False


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
    """Render a tool_use block the way Claude's UI does: a single-line
    indicator with the tool name and no arguments. Verbose argument
    summaries belong in the FTS index, not the live transcript view."""
    name = block.get("name", "?")
    return f"⏺ {name}"


def _extract_turn_text(message: Any) -> str:
    """Pull user-readable text out of a Claude transcript ``message``.

    ``message.content`` is either a plain string or a list of content
    blocks. We render:
      - ``text`` blocks: verbatim.
      - ``tool_use`` blocks: ``⏺ <ToolName>`` (one line, no args).
      - ``tool_result`` blocks: dropped entirely (Claude's own UI
        collapses them; showing them as separate ``▸ [tool_result …]``
        rows ends up looking like noise from the user side because
        Claude's protocol delivers tool results as ``type=user``
        records).

    Returns the empty string if there's nothing user-readable — the
    tailer drops empty turns so they don't render as blank rows.
    """
    if not isinstance(message, dict):
        return ""
    content = message.get("content")
    if isinstance(content, str):
        return _strip_command_xml(content) if content else ""
    if not isinstance(content, list):
        return ""
    parts: list[str] = []
    has_text = False
    for block in content:
        if not isinstance(block, dict):
            continue
        btype = block.get("type")
        if btype == "text":
            t = block.get("text")
            if isinstance(t, str) and t:
                parts.append(t)
                has_text = True
        elif btype == "tool_use":
            parts.append(_summarize_tool_use(block))
        # tool_result and unknown block types are silently dropped.
    if not has_text:
        # A turn with only tool_use blocks (no accompanying text) renders
        # as just "⏺ Bash" — visual noise. Drop it; the tailer skips
        # empty turns so they won't appear in live or history views.
        return ""
    joined = "\n".join(parts)
    return _strip_command_xml(joined) if joined else ""


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
    """Tail the JSONL for a session whose ``claude_session_id`` is known.

    Locates the file by globbing all of ``~/.claude/projects/*/<id>.jsonl``
    rather than computing an encoded subdir path — that means it works for
    any cwd Claude can store, regardless of its path-encoding scheme.
    """
    if s.claude_session_id is None:
        return
    path = find_jsonl_for_session(s.claude_session_id)
    if path is None:
        log.info("start_tailer: no JSONL on disk yet for %s; skipping", s.claude_session_id)
        return
    await _tail_jsonl(registry, s.id, path)


async def watch_for_transcript(
    registry: Registry,
    session: Session,
    *,
    claude_pid: int | None = None,
    poll_interval: float = 0.5,
    timeout: float = 60.0,
) -> None:
    """Wait for Claude to create a JSONL for ``session``, then start tailing.

    Wrapped/spawned sessions don't know their ``claude_session_id`` up
    front — Claude only writes its transcript once it's running.

    Preferred path (``claude_pid`` known): poll
    ``~/.claude/sessions/<claude_pid>.json`` — Claude itself writes this
    file with the running session's UUID, giving us a precise pid →
    sessionId mapping. This eliminates the mtime race that bites when
    another live Claude session in the same cwd keeps touching its own
    JSONL faster than the freshly-spawned one.

    Fallback (no ``claude_pid`` or pid-keyed file never appears): scan
    ``~/.claude/projects/`` for a JSONL whose first records reference
    ``session.cwd`` and whose mtime is newer than ``session.created_at``.
    This is the legacy behaviour and still works for sessions registered
    before this code was deployed.

    Once a session id is resolved we set it on the registry (which
    broadcasts ``session_id_resolved``) and spawn the regular tailer. If
    nothing shows up within ``timeout`` seconds we log and give up.
    """
    deadline = now_ms() + int(timeout * 1000)
    found: Path | None = None
    claude_session_id: str | None = None

    if claude_pid is not None:
        while now_ms() < deadline:
            sid = await asyncio.to_thread(session_id_for_pid, claude_pid)
            if sid is not None:
                jsonl = await asyncio.to_thread(find_jsonl_for_session, sid)
                if jsonl is not None:
                    claude_session_id = sid
                    found = jsonl
                    break
            await asyncio.sleep(poll_interval)

    if found is None:
        # Fallback: legacy mtime-based scan. Used when no claude_pid was
        # supplied or when the pid-keyed file never showed up.
        while now_ms() < deadline:
            found = await asyncio.to_thread(
                find_new_jsonl_for_cwd, session.cwd, session.created_at
            )
            if found is not None:
                claude_session_id = found.stem
                break
            await asyncio.sleep(poll_interval)

    if found is None or claude_session_id is None:
        log.info(
            "watch_for_transcript: no JSONL appeared for session %s within %.0fs",
            session.id,
            timeout,
        )
        return
    await registry.set_claude_session_id(session.id, claude_session_id)
    log.info(
        "watch_for_transcript: bound session %s to claude session %s",
        session.id,
        claude_session_id,
    )
    await _tail_jsonl(registry, session.id, found)
