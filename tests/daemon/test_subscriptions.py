"""Tests for SubscriptionHub fan-out."""

from __future__ import annotations

import json

from chub.daemon.subscriptions import SubscriptionHub


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
