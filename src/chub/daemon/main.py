"""`chubd` — daemon entrypoint."""

from __future__ import annotations

import asyncio
import logging
import signal
import sys
from typing import Any

from chub import __version__
from chub.daemon import paths
from chub.daemon.clock import now_ms
from chub.daemon.handlers import HandlerRegistry
from chub.daemon.pidlock import PidLockBusy, acquire
from chub.daemon.persistence import Database
from chub.daemon.registry import Registry
from chub.daemon.runs import end_run, start_run
from chub.daemon.server import Server
from chub.daemon.session import SessionKind
from chub.proto.schema import (
    ListSessionsParams,
    ListSessionsResult,
    RecolorSessionParams,
    RegisterWrappedParams,
    RegisterWrappedResult,
    RenameSessionParams,
    SessionDict,
)

log = logging.getLogger("chubd")

PROTOCOL_VERSION = 1


def _build_registry(reg: Registry) -> HandlerRegistry:
    h = HandlerRegistry()

    async def ping(params: dict[str, Any]) -> dict[str, Any]:
        return {"echo": params.get("message"), "server_time_ms": now_ms()}

    async def version(params: dict[str, Any]) -> dict[str, Any]:
        return {"version": __version__, "protocol": PROTOCOL_VERSION}

    async def register_wrapped(params: dict[str, Any]) -> dict[str, Any]:
        p = RegisterWrappedParams.model_validate(params)
        s = await reg.register(
            name=p.name, kind=SessionKind.WRAPPED, cwd=p.cwd, pid=p.pid, tags=p.tags, color=p.color
        )
        return RegisterWrappedResult(session=SessionDict(**s.to_dict())).model_dump()

    async def list_sessions(params: dict[str, Any]) -> dict[str, Any]:
        ListSessionsParams.model_validate(params)
        sessions = [SessionDict(**s.to_dict()) for s in await reg.list_all()]
        return ListSessionsResult(sessions=sessions).model_dump()

    async def rename_session(params: dict[str, Any]) -> dict[str, Any]:
        p = RenameSessionParams.model_validate(params)
        await reg.rename(p.id, p.name)
        return {}

    async def recolor_session(params: dict[str, Any]) -> dict[str, Any]:
        p = RecolorSessionParams.model_validate(params)
        await reg.recolor(p.id, p.color)
        return {}

    h.register("ping", ping)
    h.register("version", version)
    h.register("register_wrapped", register_wrapped)
    h.register("list_sessions", list_sessions)
    h.register("rename_session", rename_session)
    h.register("recolor_session", recolor_session)
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
            handlers = _build_registry(registry)
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
