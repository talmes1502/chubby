"""Wrapper-disconnect cleanup: a connection that called register_wrapped
must mark its session DEAD when it goes away, so future injects return
SESSION_DEAD instead of zombie WRAPPER_UNREACHABLE."""

from __future__ import annotations

import asyncio
from pathlib import Path

import pytest

from chub.cli.client import Client
from chub.daemon.main import serve as daemon_serve
from chub.proto.errors import ChubError, ErrorCode


async def _wait_for_socket(sock: Path) -> None:
    for _ in range(200):
        if sock.exists():
            return
        await asyncio.sleep(0.02)
    raise AssertionError(f"socket never appeared at {sock}")


async def test_wrapper_disconnect_marks_session_dead(chub_home: Path) -> None:
    sock = chub_home / "hub.sock"
    stop = asyncio.Event()
    server_task = asyncio.create_task(daemon_serve(stop_event=stop))
    try:
        await _wait_for_socket(sock)

        # --- Connection A: register a wrapped session, then drop. -----------
        wrapper = Client(sock)
        reg = await wrapper.call(
            "register_wrapped",
            {"name": "ghost", "cwd": "/tmp", "pid": 99999990},
        )
        sid = reg["session"]["id"]
        # Sanity: session is alive while wrapper is connected.
        listed = await wrapper.call("list_sessions", {})
        match = next(s for s in listed["sessions"] if s["id"] == sid)
        assert match["status"] != "dead"
        # Drop the connection abruptly.
        await wrapper.close()

        # --- Connection B: poll for the dead status (close hook is async). --
        observer = Client(sock)
        for _ in range(50):
            listed = await observer.call("list_sessions", {})
            match = next(
                (s for s in listed["sessions"] if s["id"] == sid), None
            )
            if match is not None and match["status"] == "dead":
                break
            await asyncio.sleep(0.02)
        assert match is not None
        assert match["status"] == "dead", f"expected dead, got {match!r}"

        # --- Inject must now return SESSION_DEAD, not WRAPPER_UNREACHABLE. --
        with pytest.raises(ChubError) as exc:
            await observer.call(
                "inject",
                {"session_id": sid, "payload_b64": "aGk="},
            )
        assert exc.value.code is ErrorCode.SESSION_DEAD, (
            f"expected SESSION_DEAD, got {exc.value.code!r}"
        )
        await observer.close()
    finally:
        stop.set()
        await asyncio.wait_for(server_task, timeout=3.0)
