"""`chubbyd` — daemon entrypoint."""

from __future__ import annotations

import asyncio
import base64
import json
import logging
import os
import re
import signal
import sys
from pathlib import Path
from typing import Any

from chubby import __version__
from chubby.daemon import paths
from chubby.daemon.clock import now_ms
from chubby.daemon.handlers import CallContext, HandlerRegistry
from chubby.daemon.logs import LogWriter
from chubby.daemon.persistence import Database
from chubby.daemon.pidlock import PidLockBusy, _alive, acquire
from chubby.daemon.registry import Registry
from chubby.daemon.runs import HubRun, end_run, resolve_resume, start_run
from chubby.daemon.server import Server
from chubby.daemon.session import SessionKind, SessionStatus
from chubby.daemon.subscriptions import SubscriptionHub
from chubby.proto.errors import ChubError, ErrorCode
from chubby.proto.schema import (
    AttachExistingReadonlyParams,
    AttachExistingReadonlyResult,
    AttachTmuxParams,
    AttachTmuxResult,
    DetachSessionParams,
    GetHubRunParams,
    GetHubRunResult,
    GetSessionHistoryParams,
    InjectParams,
    ListHubRunsParams,
    ListHubRunsResult,
    ListSessionsParams,
    ListSessionsResult,
    MarkIdleParams,
    PromoteSessionParams,
    PurgeParams,
    PushOutputParams,
    RecentCwdsParams,
    RecentCwdsResult,
    RecolorSessionParams,
    RefreshClaudeSessionParams,
    RegisterReadonlyParams,
    RegisterReadonlyResult,
    RegisterWrappedParams,
    RegisterWrappedResult,
    ReleaseSessionParams,
    ReleaseSessionResult,
    RenameSessionParams,
    ScanCandidatesParams,
    ScanCandidatesResult,
    SearchTranscriptsParams,
    SessionDict,
    SetHubRunNoteParams,
    SetSessionTagsParams,
    SpawnSessionParams,
    UpdateClaudePidParams,
)
from chubby.proto.rpc import Event, encode_message

log = logging.getLogger("chubbyd")

PROTOCOL_VERSION = 1


def _parse_ts_ms(raw: Any) -> int:
    """Parse a Claude JSONL timestamp. Returns 0 if unparseable."""
    if not isinstance(raw, str):
        return 0
    try:
        from datetime import datetime
        # Claude uses ISO 8601 with optional Z suffix.
        return int(datetime.fromisoformat(raw.replace("Z", "+00:00")).timestamp() * 1000)
    except (ValueError, TypeError):
        return 0


def _build_registry(
    reg: Registry, run: HubRun, db: Database, subs: SubscriptionHub
) -> HandlerRegistry:
    h = HandlerRegistry()
    background_tasks: set[asyncio.Task[None]] = set()

    async def ping(params: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        return {"echo": params.get("message"), "server_time_ms": now_ms()}

    async def version(params: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        return {"version": __version__, "protocol": PROTOCOL_VERSION}

    async def register_wrapped(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chubby.daemon import hooks as hooks_mod

        p = RegisterWrappedParams.model_validate(params)
        s = await reg.register(
            name=p.name, kind=SessionKind.WRAPPED, cwd=p.cwd, pid=p.pid, tags=p.tags, color=p.color
        )
        await reg.attach_wrapper(s.id, ctx.write)

        # When the wrapper's connection closes (daemon-side EOF, wrapper crash
        # before session_ended, etc.), mark the session DEAD and detach the
        # writer. Without this hook, future inject calls land on a zombie
        # entry that's still ``wrapped`` + ``idle`` and the user is stuck
        # with WRAPPER_UNREACHABLE on a session that looks alive.
        sid = s.id

        async def on_wrapper_disconnect() -> None:
            try:
                cur = await reg.get(sid)
            except ChubError:
                return
            if cur.status is not SessionStatus.DEAD:
                await reg.update_status(sid, SessionStatus.DEAD)
            await reg.detach_wrapper(sid)

        ctx.on_close(on_wrapper_disconnect)

        writer = LogWriter(run.dir / "logs", color=s.color, session_name=s.name)
        await reg.attach_log_writer(s.id, writer)
        # Wrapped sessions don't know their Claude session id at registration
        # time — Claude only writes its JSONL once it actually starts up.
        # Prefer the pid → sessionId mapping at ~/.claude/sessions/<pid>.json
        # when the wrapper supplied a claude_pid; that's a precise binding
        # immune to the mtime race that hits when two Claudes share a cwd.
        task = asyncio.create_task(
            hooks_mod.watch_for_transcript(reg, s, claude_pid=p.claude_pid)
        )
        background_tasks.add(task)
        task.add_done_callback(background_tasks.discard)
        return RegisterWrappedResult(session=SessionDict(**s.to_dict())).model_dump()

    async def list_sessions(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        ListSessionsParams.model_validate(params)
        sessions = [SessionDict(**s.to_dict()) for s in await reg.list_all()]
        return ListSessionsResult(sessions=sessions).model_dump()

    async def rename_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = RenameSessionParams.model_validate(params)
        await reg.rename(p.id, p.name)
        return {}

    async def recolor_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = RecolorSessionParams.model_validate(params)
        await reg.recolor(p.id, p.color)
        return {}

    async def push_output(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = PushOutputParams.model_validate(params)
        await reg.record_chunk(p.session_id, base64.b64decode(p.data_b64), role=p.role)
        return {}

    async def inject(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = InjectParams.model_validate(params)
        s = await reg.get(p.session_id)
        if s.kind is SessionKind.READONLY:
            raise ChubError(
                ErrorCode.INJECTION_NOT_SUPPORTED,
                "session is read-only; restart via chubby-claude",
            )
        if s.status is SessionStatus.DEAD:
            raise ChubError(
                ErrorCode.SESSION_DEAD,
                "session is dead; respawn it before injecting",
            )
        await reg.inject(p.session_id, base64.b64decode(p.payload_b64))
        # Mark the session thinking; the next assistant transcript_message
        # in _tail_jsonl flips it back to idle. READONLY sessions can't be
        # injected (they raise above), so this branch only fires for
        # WRAPPED/SPAWNED/TMUX_ATTACHED.
        await reg.update_status(p.session_id, SessionStatus.THINKING)
        return {}

    async def session_ended(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        sid = params["session_id"]
        await reg.update_status(sid, SessionStatus.DEAD)
        await reg.detach_wrapper(sid)
        return {}

    async def spawn_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from pydantic import ValidationError
        try:
            p = SpawnSessionParams.model_validate(params)
        except ValidationError as e:
            # Translate pydantic missing-field errors into a clean
            # INVALID_PAYLOAD instead of bubbling up as INTERNAL.
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD, f"invalid spawn params: {e}"
            ) from None
        # Sessions are organized around a project cwd (rail grouping,
        # JSONL location, hooks scope), so empty cwd has no consistent
        # meaning. The TUI modal pre-fills $HOME and the CLI defaults to
        # os.getcwd() — so this should only fire on sloppy scripted
        # invocations. Surface it loudly rather than silently default.
        cwd = p.cwd.strip()
        if not cwd:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "cwd is required (try '--cwd ~' for $HOME)",
            )
        proc_env = {
            **os.environ,
            "CHUBBY_NAME": p.name,
            "CHUBBY_HOME": str(paths.hub_home()),
        }
        # Capture wrapper+claude stderr to a per-session file so we can later
        # inspect why a wrapper exited (claude died on first-run dialog,
        # auth failure, missing binary, etc.). Without this redirect we have
        # zero visibility — the wrapper inherits DEVNULL and silent exits
        # leave us guessing.
        wrappers_dir = run.dir / "wrappers"
        wrappers_dir.mkdir(parents=True, exist_ok=True)
        safe_name = re.sub(r"[^a-zA-Z0-9_-]", "_", p.name)
        stderr_path = wrappers_dir / f"{safe_name}.stderr"
        # Pass the file object directly: asyncio dups its fd into the child,
        # so we can close our handle as soon as the spawn returns and the
        # child still has its end. Append-mode so respawn doesn't truncate.
        stderr_fp = open(stderr_path, "ab")
        try:
            await asyncio.create_subprocess_exec(
                sys.executable, "-m", "chubby.wrapper.main",
                "--name", p.name, "--cwd", cwd, "--tags", ",".join(p.tags),
                stdin=asyncio.subprocess.DEVNULL,
                stdout=stderr_fp,
                stderr=stderr_fp,
                env=proc_env,
                start_new_session=True,
            )
        finally:
            stderr_fp.close()
        for _ in range(50):
            try:
                s = await reg.get_by_name(p.name)
                return {"session": SessionDict(**s.to_dict()).model_dump()}
            except ChubError:
                await asyncio.sleep(0.1)
        raise ChubError(ErrorCode.INTERNAL, "spawned wrapper did not register")

    async def search_transcripts(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = SearchTranscriptsParams.model_validate(params)
        hub_run = None if p.all_runs else (p.hub_run_id or run.id)
        rows = await db.search(
            p.query,
            hub_run_id=hub_run,
            session_id=p.session_id,
            limit=p.limit,
        )
        return {"matches": rows}

    async def get_session_history(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Read the bound JSONL transcript and return user/assistant turns
        so the TUI can seed its viewport on startup. Returns an empty list
        if no transcript is bound (yet) or no JSONL file is found."""
        from chubby.daemon import hooks as hooks_mod

        p = GetSessionHistoryParams.model_validate(params)
        s = await reg.get(p.session_id)
        if s.claude_session_id is None:
            return {"turns": []}
        path = hooks_mod.find_jsonl_for_session(s.claude_session_id)
        if path is None:
            return {"turns": []}

        turns: list[dict[str, Any]] = []
        try:
            with open(path, encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        rec = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    t = rec.get("type")
                    if t not in ("user", "assistant"):
                        continue
                    msg = rec.get("message")
                    text = hooks_mod._extract_turn_text(msg)
                    if not text:
                        continue
                    ts_raw = rec.get("timestamp")
                    ts_ms = _parse_ts_ms(ts_raw) if ts_raw else 0
                    turns.append({
                        "role": "user" if t == "user" else "assistant",
                        "text": text,
                        "ts": ts_ms,
                    })
        except OSError:
            return {"turns": []}

        if p.limit > 0 and len(turns) > p.limit:
            turns = turns[-p.limit:]
        return {"turns": turns}

    async def register_readonly(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = RegisterReadonlyParams.model_validate(params)
        name = p.name or f"{os.path.basename(p.cwd.rstrip('/'))}-{p.claude_session_id[:4]}"
        s = await reg.register(
            name=name,
            kind=SessionKind.READONLY,
            cwd=p.cwd,
            claude_session_id=p.claude_session_id,
            tags=p.tags,
        )
        # Start the JSONL tailer for this session as a background task.
        # Imported lazily so tests can monkeypatch ``hooks.start_tailer``.
        from chubby.daemon import hooks as hooks_mod

        task = asyncio.create_task(hooks_mod.start_tailer(reg, s))
        background_tasks.add(task)
        task.add_done_callback(background_tasks.discard)
        return RegisterReadonlyResult(session=SessionDict(**s.to_dict())).model_dump()

    async def mark_idle(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = MarkIdleParams.model_validate(params)
        for s in await reg.list_all():
            if s.claude_session_id == p.claude_session_id:
                await reg.update_status(s.id, SessionStatus.AWAITING_USER)
                return {}
        return {}

    async def list_hub_runs(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        ListHubRunsParams.model_validate(params)
        return ListHubRunsResult(runs=await db.list_hub_runs()).model_dump()

    async def get_hub_run(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = GetHubRunParams.model_validate(params)
        runs = [r for r in await db.list_hub_runs() if r["id"] == p.id]
        if not runs:
            raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no hub-run {p.id}")
        sessions = [s.to_dict() for s in await db.list_sessions(hub_run_id=p.id)]
        return GetHubRunResult(run=runs[0], sessions=sessions).model_dump()

    async def set_hub_run_note(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = SetHubRunNoteParams.model_validate(params)
        await db.set_run_note(p.id, p.note)
        return {}

    async def set_session_tags(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = SetSessionTagsParams.model_validate(params)
        await reg.set_tags(p.id, add=p.add, remove=p.remove)
        return {}

    async def scan_candidates(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chubby.daemon.attach.scanner import scan

        ScanCandidatesParams.model_validate(params)
        cs = await scan()
        live = await reg.list_all()
        known_pids = {s.pid for s in live if s.pid is not None}
        return ScanCandidatesResult(
            candidates=[
                {
                    "pid": c.pid,
                    "cwd": c.cwd,
                    "tmux_target": c.tmux_target,
                    "classification": c.classification,
                    "already_attached": c.pid in known_pids,
                }
                for c in cs
            ]
        ).model_dump()

    async def attach_tmux(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chubby.daemon.attach.tmux import watch_pane

        p = AttachTmuxParams.model_validate(params)
        s = await reg.register(
            name=p.name,
            kind=SessionKind.TMUX_ATTACHED,
            cwd=p.cwd,
            pid=p.pid,
            tmux_target=p.tmux_target,
            tags=p.tags,
        )
        writer = LogWriter(run.dir / "logs", color=s.color, session_name=s.name)
        await reg.attach_log_writer(s.id, writer)
        stop = asyncio.Event()
        reg._tmux_stops[s.id] = stop
        task = asyncio.create_task(watch_pane(reg, s.id, p.tmux_target, stop=stop))
        background_tasks.add(task)
        task.add_done_callback(background_tasks.discard)
        return AttachTmuxResult(session=SessionDict(**s.to_dict())).model_dump()

    async def attach_existing_readonly(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Register a running raw ``claude`` PID as a readonly session.

        JSONL discovery is encoding-free: we scan ``~/.claude/projects/*/*.jsonl``
        for the most recent file whose first records reference this cwd.
        If none is found we still register the session — the user can still
        see it in lists; we just won't have a transcript feed.
        """
        from chubby.daemon import hooks as hooks_mod

        p = AttachExistingReadonlyParams.model_validate(params)
        name = p.name or f"{os.path.basename(p.cwd.rstrip('/'))}-{p.pid}"

        found = hooks_mod.find_new_jsonl_for_cwd(p.cwd, since_ms=0)
        claude_session_id: str | None = found.stem if found is not None else None

        s = await reg.register(
            name=name,
            kind=SessionKind.READONLY,
            cwd=p.cwd,
            pid=p.pid,
            claude_session_id=claude_session_id,
        )
        if claude_session_id is not None:
            task = asyncio.create_task(hooks_mod.start_tailer(reg, s))
            background_tasks.add(task)
            task.add_done_callback(background_tasks.discard)
        return AttachExistingReadonlyResult(
            session=SessionDict(**s.to_dict())
        ).model_dump()

    async def detach_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = DetachSessionParams.model_validate(params)
        # Resolve session (raises SESSION_NOT_FOUND if missing).
        await reg.get(p.id)
        stop = reg._tmux_stops.get(p.id)
        if stop is not None:
            stop.set()
        await reg.update_status(p.id, SessionStatus.DEAD)
        return {}

    async def release_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Release a session from chubby's management — the daemon side
        of the TUI's ``/detach`` command.

        Captures ``claude_session_id`` + ``cwd`` so the caller can
        re-open a real ``claude --resume <id>`` outside chubby, then
        SIGTERMs the wrapper (which kills its claude child), marks the
        session DEAD, and removes it from the in-memory registry. The
        on-disk JSONL transcript is untouched — the new external
        claude resumes the same conversation seamlessly.
        """
        p = ReleaseSessionParams.model_validate(params)
        s = await reg.get(p.id)
        if not s.claude_session_id:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "session has no bound claude session id yet — wait a moment and retry",
            )
        result = ReleaseSessionResult(
            claude_session_id=s.claude_session_id,
            cwd=s.cwd,
        )
        # Tell the wrapper to shut down. For WRAPPED/SPAWNED kinds we
        # push a ``shutdown`` event over the wrapper's writer; the
        # wrapper SIGTERMs its claude child and exits. For TMUX_ATTACHED
        # we just stop the watcher (we never spawned the claude). For
        # READONLY we have nothing to kill — we just drop our view.
        if s.kind in (SessionKind.WRAPPED, SessionKind.SPAWNED):
            write = reg._wrapper_writers.get(s.id)
            if write is not None:
                try:
                    await write(
                        encode_message(
                            Event(method="shutdown", params={"session_id": s.id})
                        )
                    )
                except Exception:
                    # The wrapper may already be gone; we still want to
                    # detach our local view. Don't surface this as an RPC
                    # error.
                    pass
        elif s.kind is SessionKind.TMUX_ATTACHED:
            stop = reg._tmux_stops.get(s.id)
            if stop is not None:
                stop.set()
        # Mark dead immediately + drop from in-memory registry. The
        # SQLite row stays so hub-run history still shows this session
        # ran; it just won't appear in list_sessions / the TUI rail.
        await reg.update_status(s.id, SessionStatus.DEAD)
        await reg.detach_wrapper(s.id)
        await reg.remove_session(s.id)
        return result.model_dump()

    async def promote_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chubby.daemon.attach.promote import relaunch_wrapper, wait_for_exit

        p = PromoteSessionParams.model_validate(params)
        s = await reg.get(p.id)
        if s.kind is not SessionKind.READONLY:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD, "session is not readonly"
            )
        if s.pid is None:
            await relaunch_wrapper(name=s.name, cwd=s.cwd, tags=list(s.tags))
            await reg.update_status(s.id, SessionStatus.DEAD)
            return {}
        if not await wait_for_exit(s.pid, timeout=600.0):
            raise ChubError(
                ErrorCode.INTERNAL, "timed out waiting for raw claude to exit"
            )
        await reg.update_status(s.id, SessionStatus.DEAD)
        await relaunch_wrapper(name=s.name, cwd=s.cwd, tags=list(s.tags))
        return {}

    async def purge(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = PurgeParams.model_validate(params)
        if p.run_id is None and p.session is None:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD, "specify run_id or session"
            )
        if p.run_id is not None:
            await db.conn.execute(
                "DELETE FROM transcript_fts WHERE hub_run_id = ?", (p.run_id,)
            )
            await db.conn.execute(
                "DELETE FROM sessions WHERE hub_run_id = ?", (p.run_id,)
            )
            await db.conn.execute(
                "DELETE FROM hub_runs WHERE id = ?", (p.run_id,)
            )
            await db.conn.commit()
        else:
            assert p.session is not None
            s = await reg.get_by_name(p.session)
            await db.conn.execute(
                "DELETE FROM transcript_fts WHERE session_id = ?", (s.id,)
            )
            await db.conn.execute(
                "DELETE FROM sessions WHERE id = ?", (s.id,)
            )
            await db.conn.commit()
            # Drop in-memory state too so list_sessions stops returning it.
            async with reg._lock:
                reg._by_id.pop(s.id, None)
                reg._wrapper_writers.pop(s.id, None)
                reg._writers.pop(s.id, None)
                reg._buffers.pop(s.id, None)
        return {}

    async def update_claude_pid(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Wrapper-side restart finished: refresh the daemon's view of the
        claude pid for an existing session and re-arm watch_for_transcript.

        The chubby session id is stable — same wrapper, same row, same
        connection. Only the claude pid moved. Re-watching against the
        new pid lets us re-bind the JSONL via ``~/.claude/sessions/<pid>.json``.
        With ``claude --resume <sid>`` the JSONL is the SAME file so the
        binding is effectively idempotent, but we re-run the watcher so
        the daemon's per-pid mapping is current.
        """
        from chubby.daemon import hooks as hooks_mod

        p = UpdateClaudePidParams.model_validate(params)
        s = await reg.get(p.session_id)
        # Mutate the in-memory pid; persistence is handled when the
        # transcript watcher rebinds (or via update_status next time).
        s.pid = p.claude_pid
        # Re-run the transcript watcher to rebind. The previous
        # claude_session_id is preserved on `s` so a quick lookup of
        # ~/.claude/sessions/<new_pid>.json will return the same UUID
        # (claude --resume keeps it).
        task = asyncio.create_task(
            hooks_mod.watch_for_transcript(reg, s, claude_pid=p.claude_pid)
        )
        background_tasks.add(task)
        task.add_done_callback(background_tasks.discard)
        return {}

    async def refresh_claude_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Push a ``restart_claude`` event over the wrapper's writer.

        The wrapper handles the actual SIGTERM + ``claude --resume <sid>``
        relaunch. We just bridge the TUI request to the wrapper; we don't
        wait for the wrapper to come back (that arrives later as an
        ``update_claude_pid`` call from the wrapper itself).
        """
        p = RefreshClaudeSessionParams.model_validate(params)
        s = await reg.get(p.id)
        if s.kind not in (SessionKind.WRAPPED, SessionKind.SPAWNED):
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "/refresh-claude only applies to wrapped or spawned sessions",
            )
        if s.claude_session_id is None:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "session has no bound claude session id yet; wait a moment and retry",
            )
        write = reg._wrapper_writers.get(p.id)
        if write is None:
            raise ChubError(
                ErrorCode.WRAPPER_UNREACHABLE, "wrapper not connected"
            )
        await write(
            encode_message(
                Event(
                    method="restart_claude",
                    params={"session_id": p.id},
                )
            )
        )
        return {}

    async def recent_cwds(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Return the most-recently-used cwds across all sessions.

        Used by the TUI spawn modal's Ctrl+P picker so the user can
        cycle through recent project dirs without retyping. Distinct,
        ordered most-recent-first.
        """
        p = RecentCwdsParams.model_validate(params)
        cwds = await db.recent_cwds(p.limit)
        return RecentCwdsResult(cwds=cwds).model_dump()

    async def subscribe_events(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        sub_id = await subs.subscribe(ctx.write)
        return {"subscription_id": sub_id}

    async def unsubscribe(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        sid = int(params["subscription_id"])
        await subs.unsubscribe(sid)
        return {}

    h.register("subscribe_events", subscribe_events)
    h.register("unsubscribe", unsubscribe)
    h.register("ping", ping)
    h.register("version", version)
    h.register("register_wrapped", register_wrapped)
    h.register("list_sessions", list_sessions)
    h.register("rename_session", rename_session)
    h.register("recolor_session", recolor_session)
    h.register("push_output", push_output)
    h.register("inject", inject)
    h.register("session_ended", session_ended)
    h.register("spawn_session", spawn_session)
    h.register("search_transcripts", search_transcripts)
    h.register("get_session_history", get_session_history)
    h.register("register_readonly", register_readonly)
    h.register("mark_idle", mark_idle)
    h.register("list_hub_runs", list_hub_runs)
    h.register("get_hub_run", get_hub_run)
    h.register("set_hub_run_note", set_hub_run_note)
    h.register("set_session_tags", set_session_tags)
    h.register("scan_candidates", scan_candidates)
    h.register("attach_tmux", attach_tmux)
    h.register("attach_existing_readonly", attach_existing_readonly)
    h.register("detach_session", detach_session)
    h.register("release_session", release_session)
    h.register("promote_session", promote_session)
    h.register("purge", purge)
    h.register("update_claude_pid", update_claude_pid)
    h.register("refresh_claude_session", refresh_claude_session)
    h.register("recent_cwds", recent_cwds)
    return h


async def serve(*, stop_event: asyncio.Event | None = None) -> None:
    paths.hub_home().mkdir(parents=True, exist_ok=True)
    pid_path = paths.pid_path()
    sock_path = paths.sock_path()
    stop_event = stop_event or asyncio.Event()

    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop_event.set)

    with acquire(pid_path):
        db = await Database.open(paths.state_db_path())
        resume_target = paths.chubby_env("RESUME")
        resumed_from: str | None = None
        if resume_target:
            resumed_from = await resolve_resume(db, resume_target)
        run = await start_run(db, resumed_from=resumed_from)
        try:
            subs = SubscriptionHub()
            registry = Registry(
                hub_run_id=run.id, db=db, event_log=run.event_log, subs=subs
            )
            if resumed_from:
                for prev in await db.list_sessions(hub_run_id=resumed_from):
                    if prev.kind is SessionKind.READONLY:
                        continue
                    pid_alive = prev.pid is not None and _alive(prev.pid)
                    await registry.register(
                        name=prev.name,
                        kind=prev.kind,
                        cwd=prev.cwd,
                        pid=prev.pid if pid_alive else None,
                        tags=prev.tags,
                        color=prev.color,
                    )
                    if not pid_alive:
                        s = await registry.get_by_name(prev.name)
                        await registry.update_status(s.id, SessionStatus.DEAD)
            handlers = _build_registry(registry, run, db, subs)
            server = Server(sock_path=sock_path, registry=handlers)
            await server.start()
            try:
                server_closed_task = asyncio.create_task(server.wait_closed())
                stop_task = asyncio.create_task(stop_event.wait())
                done, pending = await asyncio.wait(
                    {server_closed_task, stop_task},
                    return_when=asyncio.FIRST_COMPLETED,
                )
                for t in pending:
                    t.cancel()
                if server_closed_task in done and not stop_event.is_set():
                    log.warning(
                        "chubbyd: server closed unexpectedly; shutting down"
                    )
            finally:
                await server.stop()
        finally:
            await end_run(db, run.id)
            await db.close()


def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
    )
    try:
        asyncio.run(serve())
    except PidLockBusy as e:
        print(f"chubbyd: {e}", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main()
