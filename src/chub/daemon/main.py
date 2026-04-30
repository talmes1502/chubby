"""`chubd` — daemon entrypoint."""

from __future__ import annotations

import asyncio
import base64
import logging
import os
import signal
import sys
from pathlib import Path
from typing import Any

from chub import __version__
from chub.daemon import paths
from chub.daemon.clock import now_ms
from chub.daemon.handlers import CallContext, HandlerRegistry
from chub.daemon.logs import LogWriter
from chub.daemon.persistence import Database
from chub.daemon.pidlock import PidLockBusy, _alive, acquire
from chub.daemon.registry import Registry
from chub.daemon.runs import HubRun, end_run, resolve_resume, start_run
from chub.daemon.server import Server
from chub.daemon.session import SessionKind, SessionStatus
from chub.daemon.subscriptions import SubscriptionHub
from chub.proto.errors import ChubError, ErrorCode
from chub.proto.schema import (
    AttachExistingReadonlyParams,
    AttachExistingReadonlyResult,
    AttachTmuxParams,
    AttachTmuxResult,
    DetachSessionParams,
    GetHubRunParams,
    GetHubRunResult,
    InjectParams,
    ListHubRunsParams,
    ListHubRunsResult,
    ListSessionsParams,
    ListSessionsResult,
    MarkIdleParams,
    PromoteSessionParams,
    PurgeParams,
    PushOutputParams,
    RecolorSessionParams,
    RegisterReadonlyParams,
    RegisterReadonlyResult,
    RegisterWrappedParams,
    RegisterWrappedResult,
    RenameSessionParams,
    ScanCandidatesParams,
    ScanCandidatesResult,
    SearchTranscriptsParams,
    SessionDict,
    SetHubRunNoteParams,
    SetSessionTagsParams,
    SpawnSessionParams,
)

log = logging.getLogger("chubd")

PROTOCOL_VERSION = 1


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
        from chub.daemon import hooks as hooks_mod

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
        # Watch the projects dir for a fresh transcript and bind it.
        task = asyncio.create_task(hooks_mod.watch_for_transcript(reg, s))
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
                "session is read-only; restart via chub-claude",
            )
        if s.status is SessionStatus.DEAD:
            raise ChubError(
                ErrorCode.SESSION_DEAD,
                "session is dead; respawn it before injecting",
            )
        await reg.inject(p.session_id, base64.b64decode(p.payload_b64))
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
        p = SpawnSessionParams.model_validate(params)
        proc_env = {
            **os.environ,
            "CHUB_NAME": p.name,
            "CHUB_HOME": str(paths.hub_home()),
        }
        await asyncio.create_subprocess_exec(
            sys.executable, "-m", "chub.wrapper.main",
            "--name", p.name, "--cwd", p.cwd, "--tags", ",".join(p.tags),
            stdin=asyncio.subprocess.DEVNULL,
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL,
            env=proc_env,
            start_new_session=True,
        )
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
        from chub.daemon import hooks as hooks_mod

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
        from chub.daemon.attach.scanner import scan

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
        from chub.daemon.attach.tmux import watch_pane

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
        from chub.daemon import hooks as hooks_mod

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

    async def promote_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chub.daemon.attach.promote import relaunch_wrapper, wait_for_exit

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
    h.register("promote_session", promote_session)
    h.register("purge", purge)
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
        resume_target = os.environ.get("CHUB_RESUME")
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
                        "chubd: server closed unexpectedly; shutting down"
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
        print(f"chubd: {e}", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main()
