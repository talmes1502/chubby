"""Async RPC handler registry."""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from typing import Any

from chub.proto.errors import ChubError, ErrorCode

Handler = Callable[[dict[str, Any]], Awaitable[dict[str, Any] | None]]


class NoSuchHandler(Exception):
    pass


class HandlerRegistry:
    def __init__(self) -> None:
        self._h: dict[str, Handler] = {}

    def register(self, method: str, fn: Handler) -> None:
        if method in self._h:
            raise ValueError(f"handler {method!r} already registered")
        self._h[method] = fn

    async def invoke(self, method: str, params: dict[str, Any]) -> dict[str, Any] | None:
        fn = self._h.get(method)
        if fn is None:
            raise ChubError(ErrorCode.INVALID_PAYLOAD, f"unknown method {method!r}")
        return await fn(params)
