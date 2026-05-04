"""End-to-end test for the `attach_existing_readonly` daemon RPC.

Verifies:
1. RPC accepts (pid, cwd) and registers a readonly session even when no
   JSONL transcript exists.
2. When a JSONL transcript exists, the session is registered with the
   matching ``claude_session_id``.
3. Auto-naming uses ``<basename(cwd)>-<pid>``.
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

import pytest

from chubby.cli.client import Client
from chubby.daemon import main as chubbyd_main


async def test_attach_existing_readonly_no_transcript(
    chub_home: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """When no JSONL is found, register without claude_session_id."""
    from chubby.daemon import hooks as hooks_mod

    async def _noop(reg, s) -> None:  # type: ignore[no-untyped-def]
        return None

    monkeypatch.setattr(hooks_mod, "start_tailer", _noop)

    # Point ~/.claude/projects somewhere empty to avoid colliding with the
    # user's real transcripts.
    monkeypatch.setenv("HOME", str(tmp_path))

    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists()

    try:
        client = Client(sock)
        cwd = tmp_path / "myproj"
        cwd.mkdir()
        r = await client.call(
            "attach_existing_readonly",
            {
                "pid": 99999,
                "cwd": str(cwd),
                "name": None,
            },
        )
        s = r["session"]
        assert s["kind"] == "readonly"
        assert s["claude_session_id"] is None
        assert s["pid"] == 99999
        assert s["name"] == "myproj-99999"

        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()


async def test_attach_existing_readonly_finds_transcript(
    chub_home: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """When a JSONL referencing this cwd exists anywhere under
    ~/.claude/projects/, attach_existing_readonly should find it via the
    encoding-free cwd-field scan."""
    from chubby.daemon import hooks as hooks_mod

    async def _noop(reg, s) -> None:  # type: ignore[no-untyped-def]
        return None

    monkeypatch.setattr(hooks_mod, "start_tailer", _noop)

    cwd = tmp_path / "Some" / "Project"
    cwd.mkdir(parents=True)

    # Stash the JSONL under an arbitrary subdir name to prove the lookup
    # doesn't depend on any specific encoding scheme.
    fake_root = tmp_path / "projects-root"
    sub = fake_root / "any-encoded-name"
    sub.mkdir(parents=True)
    transcript = sub / "abc-123-456.jsonl"
    transcript.write_text(json.dumps({"type": "first", "cwd": str(cwd)}) + "\n")

    monkeypatch.setattr(hooks_mod, "claude_projects_root", lambda: fake_root)

    stop = asyncio.Event()
    server_task = asyncio.create_task(chubbyd_main.serve(stop_event=stop))
    sock = chub_home / "hub.sock"
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.05)
    assert sock.exists()

    try:
        client = Client(sock)
        r = await client.call(
            "attach_existing_readonly",
            {"pid": 4242, "cwd": str(cwd), "name": "named-explicitly"},
        )
        s = r["session"]
        assert s["kind"] == "readonly"
        assert s["claude_session_id"] == "abc-123-456"
        assert s["pid"] == 4242
        assert s["name"] == "named-explicitly"

        await client.close()
    finally:
        stop.set()
        try:
            await asyncio.wait_for(server_task, timeout=3.0)
        except TimeoutError:
            server_task.cancel()
