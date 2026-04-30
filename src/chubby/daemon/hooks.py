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

from chubby.daemon.clock import now_ms
from chubby.daemon.registry import Registry
from chubby.daemon.session import Session, SessionStatus
from chubby.proto.errors import ChubError

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
    """Legacy single-line summary kept for FTS indexing of tool-call
    turns. The live TUI no longer uses this — it consumes the
    structured tool_calls payload from ``_extract_turn_payload``."""
    name = block.get("name", "?")
    return f"⏺ {name}"


# Tool-name → input-key list to extract a one-line "summary" describing
# what the tool was called with. The TUI renders it as the second line
# of the tool-call box (e.g. "Bash command\n  ls -la"). Order in the
# tuple matters: the first key with a non-empty value wins. Tools not
# listed fall through to a generic "name=value, …" rendering.
_TOOL_SUMMARY_KEYS: dict[str, tuple[str, ...]] = {
    "Bash":      ("command",),
    "BashOutput": ("bash_id", "shell_id"),
    "Read":      ("file_path",),
    "Edit":      ("file_path",),
    "Write":     ("file_path",),
    "MultiEdit": ("file_path",),
    "NotebookEdit": ("notebook_path",),
    "Grep":      ("pattern",),
    "Glob":      ("pattern",),
    "WebFetch":  ("url",),
    "WebSearch": ("query",),
    "Task":      ("description",),
}


def _summarize_tool_input(name: str, tinput: Any) -> str:
    """Pick a single human-readable line for the tool's input args.

    For known tools we use the canonical key (Bash → command, Read →
    file_path, etc.). For TodoWrite we summarise the count; everything
    else falls through to a "key=value · key=value" render of the
    first 2 keys so the user at least sees what was passed.
    """
    if not isinstance(tinput, dict):
        return ""
    if name == "TodoWrite":
        todos = tinput.get("todos")
        if isinstance(todos, list):
            return f"{len(todos)} item{'' if len(todos) == 1 else 's'}"
    keys = _TOOL_SUMMARY_KEYS.get(name)
    if keys:
        for k in keys:
            v = tinput.get(k)
            if isinstance(v, str) and v.strip():
                return v.strip().splitlines()[0]
            if isinstance(v, int | float):
                return str(v)
    # Fallback: first 2 string-ish kv pairs, comma-joined. Truncate
    # individual values so a giant blob doesn't blow up the box.
    parts: list[str] = []
    for k, v in list(tinput.items())[:2]:
        if isinstance(v, str):
            line = v.splitlines()[0] if v else ""
            if len(line) > 80:
                line = line[:77] + "…"
            parts.append(f"{k}={line}")
        elif isinstance(v, int | float | bool):
            parts.append(f"{k}={v}")
    return " · ".join(parts)


def _summarize_tool_result(block: dict[str, Any]) -> tuple[str, bool]:
    """Pull a short preview out of a tool_result block. Returns
    ``(preview, is_error)`` — preview is the first ~3 non-blank lines,
    capped at 240 chars; is_error is True for permission rejections /
    runtime failures so the TUI can show a red ✗ instead of dim text.

    The ``is_error`` field on the block is the canonical signal. As a
    fallback we also flag well-known rejection prose (Claude emits the
    same string each time the user denies a tool use).
    """
    content = block.get("content")
    is_error = bool(block.get("is_error"))
    if isinstance(content, list):
        # tool_result content can itself be a list of {type:text, text:…}
        # sub-blocks. Concatenate the text bits.
        text = ""
        for sub in content:
            if isinstance(sub, dict) and sub.get("type") == "text":
                t = sub.get("text")
                if isinstance(t, str):
                    text += t
        content = text
    if not isinstance(content, str):
        return "", is_error
    if not is_error and "doesn't want to proceed" in content:
        is_error = True
    lines = [ln for ln in content.splitlines() if ln.strip()][:3]
    out = "\n".join(lines)
    if len(out) > 240:
        out = out[:237] + "…"
    return out, is_error


def _extract_turn_payload(
    message: Any,
) -> tuple[str, list[dict[str, Any]]]:
    """Pull both user-readable text AND structured tool calls out of a
    Claude transcript ``message``.

    Returns ``(text, tool_calls)`` where:
      - ``text`` is the concatenation of every ``text`` block (verbatim).
      - ``tool_calls`` is a list of ``{name, summary, result_preview}``
        dicts, one per ``tool_use`` block. ``result_preview`` is the
        empty string on the way out — it gets filled in by the tailer
        when the next ``tool_result`` block arrives in a later
        ``type=user`` record.

    A turn that has neither text nor tool_use blocks returns
    ``("", [])`` so the caller can skip it.
    """
    if not isinstance(message, dict):
        return "", []
    content = message.get("content")
    if isinstance(content, str):
        text = _strip_command_xml(content) if content else ""
        return text, []
    if not isinstance(content, list):
        return "", []
    text_parts: list[str] = []
    tool_calls: list[dict[str, Any]] = []
    for block in content:
        if not isinstance(block, dict):
            continue
        btype = block.get("type")
        if btype == "text":
            t = block.get("text")
            if isinstance(t, str) and t:
                text_parts.append(t)
        elif btype == "tool_use":
            name = block.get("name", "?")
            summary = _summarize_tool_input(name, block.get("input"))
            tool_calls.append({
                "id": block.get("id", ""),
                "name": name,
                "summary": summary,
                "result_preview": "",
                "result_is_error": False,
            })
    text = "\n".join(text_parts)
    if text:
        text = _strip_command_xml(text)
    return text, tool_calls


def _extract_turn_text(message: Any) -> str:
    """Back-compat wrapper for callers that only want the text body
    (FTS indexing, tests). Tool-call rendering uses
    ``_extract_turn_payload`` instead.
    """
    text, _ = _extract_turn_payload(message)
    return text


def _extract_tool_results(message: Any) -> dict[str, dict[str, Any]]:
    """When a ``type=user`` JSONL record carries tool_result blocks
    (Claude's protocol uses user-records to deliver them back), pull
    out a ``{tool_use_id: {"preview": str, "is_error": bool}}`` mapping
    so the tailer can splice each preview into the matching prior
    tool_use call (with its error state).
    """
    out: dict[str, dict[str, Any]] = {}
    if not isinstance(message, dict):
        return out
    content = message.get("content")
    if not isinstance(content, list):
        return out
    for block in content:
        if not isinstance(block, dict):
            continue
        if block.get("type") != "tool_result":
            continue
        tid = block.get("tool_use_id")
        if not isinstance(tid, str):
            continue
        preview, is_error = _summarize_tool_result(block)
        # Error rejections often have empty bodies — still surface them
        # so the user sees "✗ rejected" in the box. Plain successes
        # with empty bodies stay collapsed.
        if preview or is_error:
            out[tid] = {"preview": preview, "is_error": is_error}
    return out


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
    # Track recent tool_use calls so a follow-up tool_result block (which
    # arrives in a separate type=user record) can be spliced back as a
    # result_preview on the matching prior call. Bounded to a few
    # recent tool calls — Claude rarely keeps more than that in flight.
    recent_tool_calls: list[tuple[str, dict[str, Any]]] = []  # (sid, call)
    pending_results: dict[str, dict[str, Any]] = {}  # tool_use_id → {preview, is_error}
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
            # Side-channel: user records that carry tool_result blocks
            # update the previously-broadcast tool_use entries via a
            # dedicated event so the TUI can splice them in. Then we
            # SKIP broadcasting them as normal user turns (they have no
            # human-readable text — only the result body).
            if rtype == "user":
                results = _extract_tool_results(message)
                if results:
                    for tid, payload in results.items():
                        pending_results[tid] = payload
                        if registry.subs is not None:
                            await registry.subs.broadcast(
                                "tool_result",
                                {
                                    "session_id": session_id,
                                    "tool_use_id": tid,
                                    "preview": payload["preview"],
                                    "is_error": payload["is_error"],
                                    "ts": now_ms(),
                                },
                            )
                    # Tool-result-only user records carry no prose; skip.
                    if not isinstance(message, dict) or not isinstance(
                        message.get("content"), str
                    ):
                        continue
            text, tool_calls = _extract_turn_payload(message)
            if not text and not tool_calls:
                # Skip turns with neither text nor tool calls.
                continue
            # Splice in any results that arrived for these calls before
            # the call itself was broadcast (rare but possible at
            # startup when the JSONL is being replayed from offset 0).
            for tc in tool_calls:
                tid = tc.get("id", "")
                if tid and tid in pending_results:
                    payload = pending_results.pop(tid)
                    tc["result_preview"] = payload["preview"]
                    tc["result_is_error"] = payload["is_error"]
                recent_tool_calls.append((session_id, tc))
            if len(recent_tool_calls) > 32:
                recent_tool_calls = recent_tool_calls[-32:]
            ts = now_ms()
            if registry.db is not None:
                # Index text + a one-line tool summary so FTS can find
                # "/tag bash ls -la" on the source command, not just on
                # accompanying prose.
                fts_body = text
                if tool_calls:
                    summary_lines = "\n".join(
                        f"⏺ {tc['name']} {tc['summary']}".strip()
                        for tc in tool_calls
                    )
                    fts_body = (text + "\n" + summary_lines) if text else summary_lines
                await registry.db.insert_message(
                    session_id=session_id,
                    hub_run_id=registry.hub_run_id,
                    ts=ts,
                    role=role,
                    content=fts_body,
                )
            # Assistant turns carry a `usage` block with token counts; surface
            # it to live TUI clients as a session_usage_changed event so the
            # banner can show running token totals + tokens/sec activity. We
            # broadcast BEFORE the transcript_message so a listener that
            # arms a "thinking elapsed" timer on transcript_message has the
            # current usage totals already in hand.
            if rtype == "assistant":
                msg_obj = rec.get("message")
                if isinstance(msg_obj, dict):
                    usage = msg_obj.get("usage")
                    if isinstance(usage, dict):
                        in_raw = usage.get("input_tokens")
                        out_raw = usage.get("output_tokens")
                        cache_raw = usage.get("cache_read_input_tokens")
                        input_tokens = (
                            int(in_raw) if isinstance(in_raw, int | float) else 0
                        )
                        output_tokens = (
                            int(out_raw) if isinstance(out_raw, int | float) else 0
                        )
                        cache_read = (
                            int(cache_raw)
                            if isinstance(cache_raw, int | float)
                            else 0
                        )
                        if registry.subs is not None:
                            await registry.subs.broadcast(
                                "session_usage_changed",
                                {
                                    "session_id": session_id,
                                    "input_tokens": input_tokens,
                                    "output_tokens": output_tokens,
                                    "cache_read_input_tokens": cache_read,
                                    "ts": ts,
                                },
                            )
            if registry.subs is not None:
                await registry.subs.broadcast(
                    "transcript_message",
                    {
                        "session_id": session_id,
                        "role": role,
                        "text": text,
                        "tool_calls": tool_calls,
                        "ts": ts,
                    },
                )
            if role == "assistant":
                # Claude finished a response → flip THINKING back to idle.
                # Guarded so we don't override the Stop hook's
                # AWAITING_USER, or step on a freshly-DEAD session.
                try:
                    cur = await registry.get(session_id)
                    if cur.status is SessionStatus.THINKING:
                        await registry.update_status(
                            session_id, SessionStatus.IDLE
                        )
                except ChubError:
                    pass
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
