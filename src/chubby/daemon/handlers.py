"""Async RPC handler registry."""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any

from chubby.proto.errors import ChubError, ErrorCode


@dataclass
class CallContext:
    """Per-call context passed to every handler.

    ``connection_id`` uniquely identifies the connection within the running
    server (used to bind a session to its wrapper transport). ``write`` lets
    the handler push server-initiated frames back to the originating peer.
    ``on_close`` registers a coroutine callback to fire when the originating
    connection closes — used by handlers (e.g. ``register_wrapped``) to
    schedule cleanup that depends on the wrapper's transport staying alive.
    """

    connection_id: int
    write: Callable[[bytes], Awaitable[None]]
    on_close: Callable[[Callable[[], Awaitable[None]]], None]


Handler = Callable[[dict[str, Any], CallContext], Awaitable[dict[str, Any] | None]]


class NoSuchHandler(Exception):
    pass


class HandlerRegistry:
    def __init__(self) -> None:
        self._h: dict[str, Handler] = {}

    def register(self, method: str, fn: Handler) -> None:
        if method in self._h:
            raise ValueError(f"handler {method!r} already registered")
        self._h[method] = fn

    async def invoke(
        self,
        method: str,
        params: dict[str, Any],
        ctx: CallContext,
    ) -> dict[str, Any] | None:
        fn = self._h.get(method)
        if fn is None:
            raise ChubError(ErrorCode.INVALID_PAYLOAD, f"unknown method {method!r}")
        return await fn(params, ctx)
