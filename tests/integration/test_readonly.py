"""End-to-end test for the `register_readonly` + `mark_idle` daemon RPCs."""

from __future__ import annotations

import asyncio
import base64
from pathlib import Path

import pytest

from chub.cli.client import Client
from chub.daemon import main as chubd_main
from chub.proto.errors import ChubError, ErrorCode


async def test_register_readonly_then_inject_unsupported(
    chub_home: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # Stub start_tailer to a no-op so we don't depend on a real JSONL file.
    from chub.daemon import hooks as hooks_mod

    async def _noop(reg, s) -> None:  # type: ignore[no-untyped-def]
        return None

    monkeypatch.setattr(hooks_mod, "start_tailer", _noop)

    stop = asyncio.Event()
    server_task = asyncio.create_task(chubd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists()

    try:
        client = Client(sock)
        r = await client.call(
            "register_readonly",
            {
                "claude_session_id": "abc1234",
                "cwd": str(tmp_path),
                "name": None,
                "tags": [],
            },
        )
        s = r["session"]
        assert s["kind"] == "readonly"
        assert s["claude_session_id"] == "abc1234"
        # Auto-name from cwd basename + first 4 chars of claude_session_id.
        assert s["name"] == f"{tmp_path.name}-abc1"

        # Injection must not be supported on readonly sessions.
        with pytest.raises(ChubError) as exc:
            await client.call(
                "inject",
                {"session_id": s["id"], "payload_b64": base64.b64encode(b"x").decode()},
            )
        assert exc.value.code is ErrorCode.INJECTION_NOT_SUPPORTED

        # mark_idle should set the session to AWAITING_USER.
        await client.call("mark_idle", {"claude_session_id": "abc1234"})
        listed = await client.call("list_sessions", {})
        match = next(x for x in listed["sessions"] if x["id"] == s["id"])
        assert match["status"] == "awaiting_user"

        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
