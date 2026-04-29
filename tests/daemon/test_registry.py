import pytest

from chub.daemon.registry import Registry
from chub.daemon.session import SessionKind
from chub.proto.errors import ChubError, ErrorCode


async def test_register_wrapped_assigns_id_and_color() -> None:
    r = Registry(hub_run_id="hr_test")
    s = await r.register(name="frontend", kind=SessionKind.WRAPPED, cwd="/tmp", pid=42)
    assert s.id.startswith("s_")
    assert s.name == "frontend"
    assert s.color.startswith("#")
    assert s.kind is SessionKind.WRAPPED


async def test_register_rejects_duplicate_name() -> None:
    r = Registry(hub_run_id="hr_test")
    await r.register(name="frontend", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    with pytest.raises(ChubError) as exc:
        await r.register(name="frontend", kind=SessionKind.WRAPPED, cwd="/tmp", pid=2)
    assert exc.value.code is ErrorCode.NAME_TAKEN


async def test_list_returns_all_sessions_sorted_by_created_at() -> None:
    r = Registry(hub_run_id="hr_test")
    a = await r.register(name="a", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    b = await r.register(name="b", kind=SessionKind.WRAPPED, cwd="/tmp", pid=2)
    listed = await r.list_all()
    assert [s.id for s in listed] == [a.id, b.id]


async def test_rename_updates_name() -> None:
    r = Registry(hub_run_id="hr_test")
    s = await r.register(name="frontend", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await r.rename(s.id, "ui")
    assert (await r.get(s.id)).name == "ui"


async def test_rename_rejects_collision() -> None:
    r = Registry(hub_run_id="hr_test")
    a = await r.register(name="a", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await r.register(name="b", kind=SessionKind.WRAPPED, cwd="/tmp", pid=2)
    with pytest.raises(ChubError) as exc:
        await r.rename(a.id, "b")
    assert exc.value.code is ErrorCode.NAME_TAKEN


async def test_recolor_updates_color() -> None:
    r = Registry(hub_run_id="hr_test")
    s = await r.register(name="x", kind=SessionKind.WRAPPED, cwd="/tmp", pid=1)
    await r.recolor(s.id, "#abcdef")
    assert (await r.get(s.id)).color == "#abcdef"


async def test_get_unknown_raises() -> None:
    r = Registry(hub_run_id="hr_test")
    with pytest.raises(ChubError) as exc:
        await r.get("s_nonsense")
    assert exc.value.code is ErrorCode.SESSION_NOT_FOUND
