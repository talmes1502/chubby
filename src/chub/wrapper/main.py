"""``chub-claude`` — the wrapper. Spawns ``claude`` under a PTY, registers with
chubd, mirrors I/O bidirectionally, and listens for server-pushed
``inject_to_pty`` events. Falls back to exec'ing ``claude`` directly if chubd
is unreachable, so it stays useful even when the daemon is down.

The wrapper client uses split read/write tasks (Phase 14.2) so concurrent
``push_chunk`` calls and inbound ``inject_to_pty`` events do not contend on a
single connection lock.

Restart loop: ``_run`` registers once with chubd then enters a loop that
spawns claude per iteration via ``_run_one_claude``. When the daemon pushes
a ``restart_claude`` event (triggered by the TUI's chub-side
``/refresh-claude`` command), the loop SIGTERMs the running claude, waits
for PTY EOF, and re-launches with ``claude --resume <previous-session-id>``
so the conversation continues from the same JSONL file.
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
from chub.proto.rpc import Event
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

# Settings-error dialog: Claude prints a "Settings Error" / "Continue
# without these settings" prompt when ~/.claude/settings.json is malformed.
# Default option ("1") is "Exit" — pressing Enter would kill claude. The
# user might WANT to fix it manually, so this is opt-in via
# ``CHUB_AUTO_DISMISS_SETTINGS_ERROR=1``.
_SETTINGS_ERROR_NEEDLES = (b"settings error", b"continue without these settings")
# Time we wait for new claude's session id to land in
# ~/.claude/sessions/<pid>.json before giving up. Same budget as the
# trust-dismiss window — both are early-startup events.
_NEW_CLAUDE_SID_TIMEOUT_S = 30.0
_NEW_CLAUDE_SID_POLL_S = 0.1
# How long to wait for the PTY to drain after we SIGTERM claude during a
# restart. Claude shuts down promptly; if this expires we proceed anyway
# (the next iteration will see closed.is_set() too).
_RESTART_DRAIN_S = 5.0

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


def _read_session_id_for_pid(pid: int) -> str | None:
    """Return Claude's sessionId for ``pid`` from ~/.claude/sessions/<pid>.json,
    or None if the file is missing/malformed."""
    p = Path.home() / ".claude" / "sessions" / f"{pid}.json"
    if not p.is_file():
        return None
    try:
        import json

        data = json.loads(p.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    sid = data.get("sessionId")
    return sid if isinstance(sid, str) else None


async def _wait_for_claude_session_id(pid: int, timeout_s: float) -> str | None:
    """Poll ~/.claude/sessions/<pid>.json until ``timeout_s`` for a sessionId."""
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        sid = await asyncio.to_thread(_read_session_id_for_pid, pid)
        if sid is not None:
            return sid
        await asyncio.sleep(_NEW_CLAUDE_SID_POLL_S)
    return None


class _RunOneResult:
    """Bundle of values returned by ``_run_one_claude``: the captured
    Claude session id (None if we never saw one) and a flag set when the
    inject-listener received a ``restart_claude`` event from the daemon.
    """

    __slots__ = ("session_id", "restart_requested")

    def __init__(self) -> None:
        self.session_id: str | None = None
        self.restart_requested: bool = False


async def _run_one_claude(
    *,
    client: WrapperClient,
    cwd: str,
    passthrough: list[str],
    resume: str | None,
    is_first_iteration: bool,
    name: str,
    tags: list[str],
) -> _RunOneResult:
    """Spawn one claude under PTY, run the four pumps until EOF or a
    daemon-pushed ``restart_claude`` event, then tear it down. Returns
    a ``_RunOneResult`` carrying the captured Claude session id (so the
    next iteration can ``--resume`` it) and a flag indicating restart
    was requested."""
    result = _RunOneResult()
    restart_requested = asyncio.Event()

    claude_argv = ["claude"]
    if resume is not None:
        claude_argv += ["--resume", resume]
    claude_argv += passthrough
    _diag(f"about to spawn claude argv={claude_argv}")
    pty = PtySession(claude_argv, cwd=cwd)
    try:
        await pty.start()
    except Exception as e:
        _diag(f"pty.start() failed: {type(e).__name__}: {e}")
        raise
    _diag(f"claude pid={pty.pid}")

    if is_first_iteration:
        sid = await client.register(
            name=name,
            cwd=cwd,
            pid=pty.pid,
            tags=tags,
            claude_pid=pty.pid,
        )
        _diag(f"registered with daemon, session id={sid}")
    else:
        # Same wrapper, fresh claude — refresh the daemon's pid view so
        # watch_for_transcript can re-bind to the (likely-same) JSONL.
        await client.update_claude_pid(claude_pid=pty.pid)
        _diag(f"updated daemon claude_pid -> {pty.pid}")

    # Capture the new claude's session id as soon as it's written. We
    # stash it in result.session_id so even an exception below leaves us
    # with the most recent known id for the caller to ``--resume`` next
    # time around.
    sid_capture_task: asyncio.Task[str | None] = asyncio.create_task(
        _wait_for_claude_session_id(pty.pid, _NEW_CLAUDE_SID_TIMEOUT_S)
    )

    seq = 0
    recent_output: collections.deque[bytes] = collections.deque(maxlen=64)
    _resize_tasks: set[asyncio.Task[None]] = set()

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

    async def auto_dismiss_settings_error() -> None:
        """Opt-in: watch for a Settings Error dialog and pick "Continue
        without these settings" (option 2). Default is option 1 = Exit,
        so pressing Enter would kill claude — instead we explicitly send
        ``2\\r``. Off by default; enable with
        ``CHUB_AUTO_DISMISS_SETTINGS_ERROR=1``."""
        if os.environ.get("CHUB_AUTO_DISMISS_SETTINGS_ERROR") != "1":
            return
        needles = [_normalize_for_match(n) for n in _SETTINGS_ERROR_NEEDLES]
        deadline = time.monotonic() + _TRUST_DISMISS_WINDOW_S
        while time.monotonic() < deadline:
            await asyncio.sleep(_TRUST_DISMISS_POLL_S)
            if pty.closed.is_set():
                return
            joined = _normalize_for_match(b"".join(recent_output))
            if any(n in joined for n in needles):
                _diag("settings-error dialog detected — sending '2'")
                await pty.write_user(b"2\r")
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
            if not isinstance(msg, Event):
                continue
            if msg.method == "inject_to_pty":
                payload = base64.b64decode(msg.params["payload_b64"])
                if not payload.endswith(b"\n") and not payload.endswith(b"\r"):
                    payload += b"\r"
                await pty.write_user(payload)
            elif msg.method == "restart_claude":
                _diag("restart_claude event received — SIGTERMing claude")
                result.restart_requested = True
                restart_requested.set()
                # SIGTERM the claude child; the PTY pump will see EOF and
                # return, which trips the gather() below.
                pty.signal_child(signal.SIGTERM)
                # Don't return — keep listening so a second restart event
                # during teardown still wins (idempotent: same effect).

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

    pump_task = asyncio.create_task(pump_pty_to_daemon_and_term())
    stdin_task = asyncio.create_task(pump_term_to_pty())
    inject_task = asyncio.create_task(listen_for_inject())
    trust_task = asyncio.create_task(auto_dismiss_trust_prompt())
    settings_task = asyncio.create_task(auto_dismiss_settings_error())

    try:
        # The PTY pump returns when claude exits (EOF). The other tasks
        # keep going until cancelled — we cancel them once the pump
        # returns. We also stop early if a restart was requested AND the
        # PTY drained.
        try:
            await asyncio.wait_for(pump_task, timeout=None)
        except asyncio.CancelledError:
            raise
        except Exception as e:
            _diag(f"pty pump raised: {type(e).__name__}: {e}")

        if pty.closed.is_set():
            _diag(
                "claude exited (PTY EOF)"
                + (" after restart_claude" if result.restart_requested else "")
            )
        else:
            _diag("pty pump returned without EOF — unusual")

        # If restart was requested, wait briefly for the drain. The pump
        # has already returned; this is just a defensive backstop.
        if result.restart_requested:
            try:
                await asyncio.wait_for(
                    pty.closed.wait(), timeout=_RESTART_DRAIN_S
                )
            except TimeoutError:
                _diag("restart drain timeout — proceeding anyway")
    finally:
        for t in (stdin_task, inject_task, trust_task, settings_task):
            if not t.done():
                t.cancel()
        for t in (stdin_task, inject_task, trust_task, settings_task):
            try:
                await t
            except (asyncio.CancelledError, Exception):
                pass
        # Capture the latest known session id; cancel the capture task
        # if it's still polling.
        if sid_capture_task.done():
            try:
                result.session_id = sid_capture_task.result()
            except Exception:
                result.session_id = None
        else:
            sid_capture_task.cancel()
            try:
                result.session_id = await sid_capture_task
            except (asyncio.CancelledError, Exception):
                result.session_id = None
        # Final teardown: make sure the claude child is gone. terminate
        # is idempotent on already-dead processes (errno.ESRCH is swallowed).
        await pty.terminate()
    return result


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

    tags = [t for t in args.tags.split(",") if t]
    resume: str | None = None
    is_first = True
    try:
        while True:
            res = await _run_one_claude(
                client=client,
                cwd=args.cwd,
                passthrough=passthrough,
                resume=resume,
                is_first_iteration=is_first,
                name=name,
                tags=tags,
            )
            is_first = False
            if not res.restart_requested:
                _diag("wrapper exiting: claude exited normally")
                return 0
            # Prefer the freshly-captured sessionId; fall back to the
            # previous one if capture failed (network blip, claude bailed
            # before writing the per-pid file). Either way we re-enter
            # with --resume so the JSONL continues.
            if res.session_id is not None:
                resume = res.session_id
            _diag(f"restarting claude with --resume {resume!r}")
    finally:
        await client.session_ended()
        await client.close()


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
