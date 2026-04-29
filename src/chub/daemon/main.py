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
from chub.daemon.server import Server

log = logging.getLogger("chubd")

PROTOCOL_VERSION = 1


def _build_registry() -> HandlerRegistry:
    reg = HandlerRegistry()

    async def ping(params: dict[str, Any]) -> dict[str, Any]:
        return {"echo": params.get("message"), "server_time_ms": now_ms()}

    async def version(params: dict[str, Any]) -> dict[str, Any]:
        return {"version": __version__, "protocol": PROTOCOL_VERSION}

    reg.register("ping", ping)
    reg.register("version", version)
    return reg


async def serve(*, stop_event: asyncio.Event | None = None) -> None:
    paths.hub_home().mkdir(parents=True, exist_ok=True)
    pid_path = paths.pid_path()
    sock_path = paths.sock_path()
    stop_event = stop_event or asyncio.Event()

    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop_event.set)

    with acquire(pid_path):
        registry = _build_registry()
        server = Server(sock_path=sock_path, registry=registry)
        await server.start()
        try:
            await stop_event.wait()
        finally:
            await server.stop()


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
