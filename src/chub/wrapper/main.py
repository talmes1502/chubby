"""``chub-claude`` — the wrapper. Spawns ``claude`` under a PTY, registers with
chubd, mirrors I/O bidirectionally, and listens for server-pushed
``inject_to_pty`` events. Falls back to exec'ing ``claude`` directly if chubd
is unreachable, so it stays useful even when the daemon is down.

V1 limitation: the daemon-call lock and the inbound read loop share the same
``WrapperClient`` connection; under load a ``push_chunk`` and an inbound
``inject_to_pty`` can race. Tracked in plan Phase 14 polish.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import os
import shutil
import signal
import struct
import sys
from pathlib import Path
from typing import Any

from chub.daemon import paths
from chub.proto import frame
from chub.proto.errors import ChubError
from chub.proto.rpc import Event, decode_message
from chub.wrapper.client import WrapperClient
from chub.wrapper.pty import PtySession


def parse_args(argv: list[str]) -> tuple[argparse.Namespace, list[str]]:
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("--name", default=os.environ.get("CHUB_NAME"))
    p.add_argument("--cwd", default=os.getcwd())
    p.add_argument("--tags", default="")
    return p.parse_known_args(argv[1:])


def _exec_claude_directly(extra_argv: list[str]) -> None:
    claude = shutil.which("claude")
    if not claude:
        sys.exit("chub-claude: 'claude' binary not found in PATH")
    os.execv(claude, ["claude", *extra_argv])


async def _run() -> int:
    args, passthrough = parse_args(sys.argv)
    name = args.name
    if not name:
        sys.stderr.write("session name: ")
        sys.stderr.flush()
        name = sys.stdin.readline().strip()
    if not name:
        sys.exit("chub-claude: --name or CHUB_NAME required")

    sock = Path(os.environ.get("CHUB_SOCK", str(paths.sock_path())))
    client = WrapperClient(sock)
    try:
        await asyncio.wait_for(client._ensure(), timeout=2.0)
    except (FileNotFoundError, ConnectionRefusedError, TimeoutError):
        sys.stderr.write(
            f"chub-claude: chubd not running on {sock}; running plain claude\n"
        )
        _exec_claude_directly(passthrough)
        return 0

    pty = PtySession(["claude", *passthrough], cwd=args.cwd)
    await pty.start()
    _resize_tasks: set[asyncio.Task[None]] = set()

    await client.register(
        name=name,
        cwd=args.cwd,
        pid=pty.pid,
        tags=[t for t in args.tags.split(",") if t],
    )

    seq = 0

    async def pump_pty_to_daemon_and_term() -> None:
        nonlocal seq
        async for chunk in pty.iter_output():
            try:
                os.write(sys.stdout.fileno(), chunk)
            except OSError:
                pass
            seq += 1
            try:
                await client.push_chunk(seq=seq, data=chunk)
            except ChubError:
                # Daemon disappeared; keep running, buffer dropped (V1).
                pass

    async def pump_term_to_pty() -> None:
        loop = asyncio.get_running_loop()

        def _read_stdin() -> bytes:
            buf = sys.stdin.buffer
            read1 = getattr(buf, "read1", None)
            if read1 is not None:
                return bytes(read1(4096))
            return bytes(buf.read(4096))

        while not pty.closed.is_set():
            chunk = await loop.run_in_executor(None, _read_stdin)
            if not chunk:
                return
            await pty.write_user(chunk)

    async def listen_for_inject() -> None:
        # Reuse the WrapperClient's connection: it owns the framed stream.
        while True:
            await client._ensure()
            reader = client._reader
            if reader is None:
                await asyncio.sleep(0.5)
                continue
            try:
                raw = await frame.read_frame(reader)
            except (ConnectionResetError, BrokenPipeError):
                client._reader = client._writer = None
                await asyncio.sleep(0.5)
                continue
            if raw is None:
                await asyncio.sleep(0.5)
                continue
            try:
                msg = decode_message(raw)
            except ChubError:
                continue
            if isinstance(msg, Event) and msg.method == "inject_to_pty":
                payload = base64.b64decode(msg.params["payload_b64"])
                if not payload.endswith(b"\n") and not payload.endswith(b"\r"):
                    payload += b"\r"
                await pty.write_user(payload)

    def on_winch(*_args: Any) -> None:
        try:
            import fcntl
            import termios

            buf = fcntl.ioctl(sys.stdout.fileno(), termios.TIOCGWINSZ, b"\x00" * 8)
            rows, cols = struct.unpack("HHHH", buf)[:2]
            t = asyncio.create_task(pty.resize(rows, cols))
            _resize_tasks.add(t)
            t.add_done_callback(_resize_tasks.discard)
        except Exception:
            pass

    try:
        signal.signal(signal.SIGWINCH, on_winch)
    except (AttributeError, ValueError):
        pass
    on_winch()

    try:
        await asyncio.gather(
            pump_pty_to_daemon_and_term(),
            pump_term_to_pty(),
            listen_for_inject(),
            return_exceptions=True,
        )
    finally:
        await client.session_ended()
        await client.close()
        await pty.terminate()
    return 0


def main() -> None:
    try:
        sys.exit(asyncio.run(_run()))
    except KeyboardInterrupt:
        sys.exit(130)


if __name__ == "__main__":
    main()
