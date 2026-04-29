"""`chubd` — daemon entrypoint."""

from __future__ import annotations

import asyncio
import base64
import logging
import os
import signal
import sys
from typing import Any

from chub import __version__
from chub.daemon import paths
from chub.daemon.clock import now_ms
from chub.daemon.handlers import CallContext, HandlerRegistry
from chub.daemon.logs import LogWriter
from chub.daemon.persistence import Database
from chub.daemon.pidlock import PidLockBusy, acquire
from chub.daemon.registry import Registry
from chub.daemon.runs import HubRun, end_run, start_run
from chub.daemon.server import Server
from chub.daemon.session import SessionKind, SessionStatus
from chub.proto.errors import ChubError, ErrorCode
from chub.proto.schema import (
    InjectParams,
    ListSessionsParams,
    ListSessionsResult,
    MarkIdleParams,
    PushOutputParams,
    RecolorSessionParams,
    RegisterReadonlyParams,
    RegisterReadonlyResult,
    RegisterWrappedParams,
    RegisterWrappedResult,
    RenameSessionParams,
    SearchTranscriptsParams,
    SessionDict,
    SpawnSessionParams,
)

log = logging.getLogger("chubd")

PROTOCOL_VERSION = 1


def _build_registry(
    reg: Registry, run: HubRun, db: Database
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
        p = RegisterWrappedParams.model_validate(params)
        s = await reg.register(
            name=p.name, kind=SessionKind.WRAPPED, cwd=p.cwd, pid=p.pid, tags=p.tags, color=p.color
        )
        await reg.attach_wrapper(s.id, ctx.write)
        writer = LogWriter(run.dir / "logs", color=s.color, session_name=s.name)
        await reg.attach_log_writer(s.id, writer)
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
        run = await start_run(db)
        try:
            registry = Registry(hub_run_id=run.id, db=db, event_log=run.event_log)
            handlers = _build_registry(registry, run, db)
            server = Server(sock_path=sock_path, registry=handlers)
            await server.start()
            try:
                await stop_event.wait()
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
