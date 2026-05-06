"""Tests for SubscriptionHub fan-out."""

from __future__ import annotations

import json

from chubby.daemon.subscriptions import SubscriptionHub


async def test_subscribe_and_broadcast() -> None:
    hub = SubscriptionHub()
    captured: list[bytes] = []

    async def write(b: bytes) -> None:
        captured.append(b)

    sid = await hub.subscribe(write)
    assert sid == 1
    await hub.broadcast("session_added", {"id": "s_1", "name": "frontend"})
    assert len(captured) == 1
    msg = json.loads(captured[0])
    assert msg["jsonrpc"] == "2.0"
    assert msg["method"] == "event"
    assert msg["params"]["subscription_id"] == 1
    assert msg["params"]["event_method"] == "session_added"
    assert msg["params"]["event_params"] == {"id": "s_1", "name": "frontend"}


async def test_unsubscribe_stops_delivery() -> None:
    hub = SubscriptionHub()
    captured: list[bytes] = []

    async def write(b: bytes) -> None:
        captured.append(b)

    sid = await hub.subscribe(write)
    await hub.unsubscribe(sid)
    await hub.broadcast("anything", {})
    assert captured == []


async def test_broadcast_fans_out_to_multiple_subs() -> None:
    hub = SubscriptionHub()
    a: list[bytes] = []
    b: list[bytes] = []

    async def wa(p: bytes) -> None:
        a.append(p)

    async def wb(p: bytes) -> None:
        b.append(p)

    sid_a = await hub.subscribe(wa)
    sid_b = await hub.subscribe(wb)
    assert sid_a != sid_b
    await hub.broadcast("session_status_changed", {"id": "s_x", "status": "idle"})
    assert len(a) == 1
    assert len(b) == 1
    msg_a = json.loads(a[0])
    msg_b = json.loads(b[0])
    assert msg_a["params"]["subscription_id"] == sid_a
    assert msg_b["params"]["subscription_id"] == sid_b


async def test_broken_pipe_auto_unsubscribes() -> None:
    hub = SubscriptionHub()

    async def writer(p: bytes) -> None:
        raise BrokenPipeError()

    sid = await hub.subscribe(writer)
    await hub.broadcast("session_added", {})
    # subsequent broadcasts are no-ops because the sub was removed.
    assert sid not in hub._subs


async def test_enotconn_auto_unsubscribes() -> None:
    """ENOTCONN (Errno 57, "Socket is not connected") on a sub's
    transport must NOT crash the broadcast — drop the sub and carry
    on. Pre-fix this raised through register_wrapped, which the
    daemon then surfaced as INTERNAL, killing a freshly-spawning
    wrapper before claude could even register."""
    import errno

    hub = SubscriptionHub()
    raised = False

    async def writer(p: bytes) -> None:
        nonlocal raised
        raised = True
        # Plain OSError(ENOTCONN) — what asyncio raises when you
        # try to write to a unix socket whose peer-side has gone
        # into a half-open state.
        raise OSError(errno.ENOTCONN, "Socket is not connected")

    sid = await hub.subscribe(writer)
    # Must not raise.
    await hub.broadcast("session_added", {})
    assert raised, "writer should have been called once"
    assert sid not in hub._subs, "broken sub must be unsubscribed"


async def test_one_broken_sub_does_not_block_others() -> None:
    """A broken sub at the front of the iteration must not stop
    later subs from receiving the broadcast."""
    import errno

    hub = SubscriptionHub()
    bad_seen = 0
    good: list[bytes] = []

    async def bad(p: bytes) -> None:
        nonlocal bad_seen
        bad_seen += 1
        raise OSError(errno.ENOTCONN, "Socket is not connected")

    async def healthy(p: bytes) -> None:
        good.append(p)

    bad_sid = await hub.subscribe(bad)
    good_sid = await hub.subscribe(healthy)
    await hub.broadcast("session_added", {})
    assert bad_seen == 1
    assert len(good) == 1, "healthy sub must still receive"
    assert bad_sid not in hub._subs
    assert good_sid in hub._subs
