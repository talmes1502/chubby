from __future__ import annotations

from typing import Any

import pytest

from chubby.daemon.handlers import CallContext, HandlerRegistry
from chubby.proto.errors import ChubError, ErrorCode


def _stub_ctx() -> CallContext:
    async def _noop(_b: bytes) -> None:
        return None

    return CallContext(connection_id=0, write=_noop, on_close=lambda _cb: None)


async def test_register_and_invoke() -> None:
    r = HandlerRegistry()

    async def ping(params: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        return {"echo": params.get("message")}

    r.register("ping", ping)
    out = await r.invoke("ping", {"message": "hi"}, _stub_ctx())
    assert out == {"echo": "hi"}


async def test_invoke_unknown_method_raises_chuberror() -> None:
    r = HandlerRegistry()
    with pytest.raises(ChubError) as exc:
        await r.invoke("nope", {}, _stub_ctx())
    assert exc.value.code is ErrorCode.INVALID_PAYLOAD


async def test_register_duplicate_raises() -> None:
    r = HandlerRegistry()

    async def h1(_p: dict[str, Any], _c: CallContext) -> dict[str, Any]:
        return {"x": 1}

    async def h2(_p: dict[str, Any], _c: CallContext) -> dict[str, Any]:
        return {"x": 2}

    r.register("ping", h1)
    with pytest.raises(ValueError):
        r.register("ping", h2)
