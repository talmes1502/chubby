"""Per-connection event-stream fan-out."""

from __future__ import annotations

import asyncio
import logging
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any

log = logging.getLogger(__name__)

WriteFn = Callable[[bytes], Awaitable[None]]


@dataclass
class Subscription:
    sub_id: int
    write: WriteFn


class SubscriptionHub:
    """Fan-out for server-pushed events to subscribed connections.

    Each subscription is a (sub_id, write_fn) pair; ``broadcast`` serialises
    the event once per subscriber so each connection sees its own
    ``subscription_id``. Writes that fail with a connection error trigger
    automatic unsubscription.
    """

    def __init__(self) -> None:
        self._subs: dict[int, Subscription] = {}
        self._next_id = 1
        self._lock = asyncio.Lock()

    async def subscribe(self, write: WriteFn) -> int:
        async with self._lock:
            sub_id = self._next_id
            self._next_id += 1
            self._subs[sub_id] = Subscription(sub_id, write)
        return sub_id

    async def unsubscribe(self, sub_id: int) -> None:
        async with self._lock:
            self._subs.pop(sub_id, None)

    async def broadcast(self, event_method: str, params: dict[str, Any]) -> None:
        from chubby.proto.rpc import Event, encode_message

        async with self._lock:
            subs = list(self._subs.values())
        for s in subs:
            payload = encode_message(
                Event(
                    method="event",
                    params={
                        "subscription_id": s.sub_id,
                        "event_method": event_method,
                        "event_params": params,
                    },
                )
            )
            try:
                await s.write(payload)
            except (ConnectionResetError, BrokenPipeError):
                await self.unsubscribe(s.sub_id)
