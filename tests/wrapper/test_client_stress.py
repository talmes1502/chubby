"""Stress test for the split read/write WrapperClient (Phase 14.2).

Concurrent push_chunks issued from the wrapper while the daemon pushes inbound
inject_to_pty events on the same connection. With the old single-lock client
this would deadlock; with split read/write tasks it must complete well within
the 5s budget.
"""

from __future__ import annotations

import asyncio
import base64
import shutil
import tempfile
from pathlib import Path
from typing import Any

from chub.daemon.handlers import CallContext, HandlerRegistry
from chub.daemon.server import Server
from chub.proto.rpc import Event, encode_message
from chub.wrapper.client import WrapperClient


async def test_concurrent_push_and_inbound_inject_no_deadlock() -> None:
    short_dir = Path(tempfile.mkdtemp(prefix="chub-"))
    try:
        sock_path = short_dir / "h.sock"
        reg = HandlerRegistry()
        received_chunks: list[int] = []
        # Track the wrapper's connection write closure so the test can push
        # server-initiated events onto it.
        write_closures: list[CallContext] = []

        async def register_wrapped(p: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
            write_closures.append(ctx)
            return {
                "session": {
                    "id": "s_1",
                    "hub_run_id": "hr_1",
                    "name": p["name"],
                    "color": "#5fafff",
                    "kind": "wrapped",
                    "cwd": p["cwd"],
                    "created_at": 0,
                    "last_activity_at": 0,
                    "status": "idle",
                    "pid": p["pid"],
                    "claude_session_id": None,
                    "tmux_target": None,
                    "tags": [],
                    "ended_at": None,
                }
            }

        async def push_output(p: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
            received_chunks.append(p["seq"])
            return {}

        reg.register("register_wrapped", register_wrapped)
        reg.register("push_output", push_output)

        s = Server(sock_path=sock_path, registry=reg)
        await s.start()
        try:
            client = WrapperClient(sock_path)
            await client.register(name="x", cwd="/tmp", pid=1, tags=[])

            # Wait until the daemon has captured the wrapper's write closure.
            for _ in range(50):
                if write_closures:
                    break
                await asyncio.sleep(0.01)
            assert write_closures, "register_wrapped never observed"
            wrapper_ctx = write_closures[0]

            events = await client.events()

            # Background: 100 concurrent push_chunks from the client.
            async def burst() -> None:
                tasks = [
                    asyncio.create_task(
                        client.push_chunk(seq=i, data=f"chunk-{i}".encode())
                    )
                    for i in range(1, 101)
                ]
                await asyncio.gather(*tasks)

            # Background: 10 server-pushed inject_to_pty events interleaved.
            async def inject_burst() -> None:
                for i in range(10):
                    payload = base64.b64encode(f"prompt-{i}\n".encode()).decode()
                    await wrapper_ctx.write(
                        encode_message(
                            Event(
                                method="inject_to_pty",
                                params={"session_id": "s_1", "payload_b64": payload},
                            )
                        )
                    )
                    await asyncio.sleep(0)

            # Background: drain inbound events as the wrapper would.
            inbound: list[Event] = []

            async def drain() -> None:
                while len(inbound) < 10:
                    inbound.append(await events.get())

            await asyncio.wait_for(
                asyncio.gather(burst(), inject_burst(), drain()),
                timeout=5.0,
            )
            await client.close()

            # Every push made it across, every event was delivered.
            assert sorted(received_chunks) == list(range(1, 101))
            assert len(inbound) == 10
            assert all(ev.method == "inject_to_pty" for ev in inbound)
        finally:
            await s.stop()
    finally:
        shutil.rmtree(short_dir, ignore_errors=True)
