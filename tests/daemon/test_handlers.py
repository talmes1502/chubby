import pytest

from chub.daemon.handlers import HandlerRegistry, NoSuchHandler
from chub.proto.errors import ChubError, ErrorCode


async def test_register_and_invoke() -> None:
    r = HandlerRegistry()

    async def ping(params: dict) -> dict:
        return {"echo": params.get("message")}

    r.register("ping", ping)
    out = await r.invoke("ping", {"message": "hi"})
    assert out == {"echo": "hi"}


async def test_invoke_unknown_method_raises_chuberror() -> None:
    r = HandlerRegistry()
    with pytest.raises(ChubError) as exc:
        await r.invoke("nope", {})
    assert exc.value.code is ErrorCode.INVALID_PAYLOAD


async def test_register_duplicate_raises() -> None:
    r = HandlerRegistry()
    r.register("ping", lambda p: {"x": 1})  # type: ignore[arg-type]
    with pytest.raises(ValueError):
        r.register("ping", lambda p: {"x": 2})  # type: ignore[arg-type]
