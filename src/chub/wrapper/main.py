"""``chub-claude`` — the wrapper. Spawns ``claude`` under a PTY, registers with
chubd, mirrors I/O bidirectionally, and listens for server-pushed
``inject_to_pty`` events. Falls back to exec'ing ``claude`` directly if chubd
is unreachable, so it stays useful even when the daemon is down.

The wrapper client uses split read/write tasks (Phase 14.2) so concurrent
``push_chunk`` calls and inbound ``inject_to_pty`` events do not contend on a
single connection lock.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import collections
import os
import re
import shutil
import signal
import struct
import sys
import time
from pathlib import Path
from typing import Any

from chub.daemon import paths
from chub.proto.errors import ChubError
from chub.wrapper.client import WrapperClient
from chub.wrapper.pty import PtySession

# Phrase Claude prints in its first-run "Quick safety check: Is this a
# project you created or one you trust?" dialog. We match the unique
# substring "trust this folder" and accept the default ("Yes") by
# pressing Enter. We deliberately do NOT match a bare "trust" token —
# Claude could conceivably show another trust-related dialog whose
# default is "No", and pressing Enter on that would be wrong.
_TRUST_PROMPT_NEEDLE = b"trust this folder"
_TRUST_DISMISS_WINDOW_S = 5.0
_TRUST_DISMISS_POLL_S = 0.2

# Strip the most common visual ANSI escape sequences before substring
# matching: CSI ``...`` letter (covers SGR colors, cursor moves like
# ``\x1b[1C`` that Claude uses to render words with single-space gaps),
# and OSC ``...`` BEL/ST hyperlinks. After stripping, words that looked
# like ``trust\x1b[1Cthis\x1b[1Cfolder`` collapse to ``trustthisfolder``,
# which we normalize by also stripping spaces from the needle/haystack.
_ANSI_CSI_RE = re.compile(rb"\x1b\[[0-9;?]*[A-Za-z]")
_ANSI_OSC_RE = re.compile(rb"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)")
_WHITESPACE_RE = re.compile(rb"\s+")


def _normalize_for_match(buf: bytes) -> bytes:
    """Strip ANSI escapes and whitespace so Claude's PTY-rendered prompts
    can be matched as plain substrings regardless of how the renderer
    spaced the words."""
    buf = _ANSI_CSI_RE.sub(b"", buf)
    buf = _ANSI_OSC_RE.sub(b"", buf)
    buf = _WHITESPACE_RE.sub(b"", buf)
    return buf.lower()


def _diag(msg: str) -> None:
    """Append a timestamped diagnostic line to wrapper stderr.

    Daemon-spawned wrappers have stderr redirected to a per-session file
    (see ``spawn_session`` in chub.daemon.main). These breadcrumbs let
    ``chub diag <name>`` answer "why did this session die?" without us
    having to guess.
    """
    sys.stderr.write(f"[chub-claude {time.strftime('%H:%M:%S')}] {msg}\n")
    sys.stderr.flush()


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

    _diag(f"starting: name={name} cwd={args.cwd} passthrough={passthrough}")

    sock = Path(os.environ.get("CHUB_SOCK", str(paths.sock_path())))
    client = WrapperClient(sock)
    try:
        await asyncio.wait_for(client._ensure(), timeout=2.0)
    except (FileNotFoundError, ConnectionRefusedError, TimeoutError):
        _diag(f"chubd not reachable on {sock}; exec'ing plain claude")
        sys.stderr.write(
            f"chub-claude: chubd not running on {sock}; running plain claude\n"
        )
        _exec_claude_directly(passthrough)
        return 0

    claude_argv = ["claude", *passthrough]
    _diag(f"about to spawn claude argv={claude_argv}")
    pty = PtySession(claude_argv, cwd=args.cwd)
    try:
        await pty.start()
    except Exception as e:
        _diag(f"pty.start() failed: {type(e).__name__}: {e}")
        raise
    _diag(f"claude pid={pty.pid}")
    _resize_tasks: set[asyncio.Task[None]] = set()

    sid = await client.register(
        name=name,
        cwd=args.cwd,
        pid=pty.pid,
        tags=[t for t in args.tags.split(",") if t],
        claude_pid=pty.pid,
    )
    _diag(f"registered with daemon, session id={sid}")

    seq = 0
    # Bounded sliding window of recent PTY output, fed by the main pump.
    # auto_dismiss_trust_prompt scans this for the trust-dialog needle.
    # 64 chunks * 4 KiB max chunk = ~256 KiB worst case, in practice far
    # less; we only keep the tail because the prompt appears very early.
    recent_output: collections.deque[bytes] = collections.deque(maxlen=64)

    async def pump_pty_to_daemon_and_term() -> None:
        nonlocal seq
        async for chunk in pty.iter_output():
            try:
                os.write(sys.stdout.fileno(), chunk)
            except OSError:
                pass
            recent_output.append(chunk)
            seq += 1
            try:
                await client.push_chunk(seq=seq, data=chunk)
            except ChubError:
                # Daemon disappeared; keep running, buffer dropped (V1).
                pass

    async def auto_dismiss_trust_prompt() -> None:
        """Watch the PTY output for Claude's first-run "trust this folder?"
        dialog and accept the default ("Yes") by pressing Enter.

        Daemon-spawned wrappers run with stdin=DEVNULL, so without this the
        dialog blocks forever — claude is alive but the user sees a blank
        viewport with no input being received. This bails out after
        ``_TRUST_DISMISS_WINDOW_S`` seconds either way: if the dialog
        didn't appear by then, the user is past the first-run gate.

        Disabled when ``CHUB_NO_AUTO_TRUST=1`` is set in the environment.
        """
        if os.environ.get("CHUB_NO_AUTO_TRUST") == "1":
            return
        # Normalize the needle the same way as the haystack so the spaces
        # in ``trust this folder`` don't trip on Claude's per-letter cursor
        # advance rendering.
        needle = _normalize_for_match(_TRUST_PROMPT_NEEDLE)
        deadline = time.monotonic() + _TRUST_DISMISS_WINDOW_S
        while time.monotonic() < deadline:
            await asyncio.sleep(_TRUST_DISMISS_POLL_S)
            if pty.closed.is_set():
                return
            joined = _normalize_for_match(b"".join(recent_output))
            if needle in joined:
                _diag("trust dialog detected — sending Enter")
                await pty.write_user(b"\r")
                return

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
        # Consume server-pushed events from the client's inbound queue.
        # The client's reader task does the framing/decoding for us.
        events = await client.events()
        while True:
            try:
                msg = await events.get()
            except asyncio.CancelledError:
                raise
            if msg.method == "inject_to_pty":
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
        results = await asyncio.gather(
            pump_pty_to_daemon_and_term(),
            pump_term_to_pty(),
            listen_for_inject(),
            auto_dismiss_trust_prompt(),
            return_exceptions=True,
        )
        # Surface unexpected exceptions in the gather. ``return_exceptions``
        # converts task failures into return values rather than tearing down
        # the gather, so without this they'd vanish silently.
        for label, r in zip(
            ("pty_pump", "stdin_pump", "inject_listener", "trust_dismiss"),
            results,
        ):
            if isinstance(r, BaseException) and not isinstance(
                r, asyncio.CancelledError
            ):
                _diag(f"task {label} raised: {type(r).__name__}: {r}")
        if pty.closed.is_set():
            _diag("wrapper exiting: PTY EOF (claude exited)")
        else:
            _diag("wrapper exiting: gather returned (no PTY EOF)")
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
    except SystemExit:
        raise
    except BaseException as e:
        # Last-resort breadcrumb so a crash in startup (e.g., missing
        # claude binary, exec failure) actually shows up in the diag log.
        import traceback

        sys.stderr.write(
            f"[chub-claude] fatal: {type(e).__name__}: {e}\n"
            f"{traceback.format_exc()}\n"
        )
        sys.stderr.flush()
        raise


if __name__ == "__main__":
    main()
