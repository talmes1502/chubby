"""``chubby-claude`` — the wrapper. Spawns ``claude`` under a PTY, registers with
chubbyd, mirrors I/O bidirectionally, and listens for server-pushed
``inject_to_pty`` events. Falls back to exec'ing ``claude`` directly if chubbyd
is unreachable, so it stays useful even when the daemon is down.

The wrapper client uses split read/write tasks (Phase 14.2) so concurrent
``push_chunk`` calls and inbound ``inject_to_pty`` events do not contend on a
single connection lock.

Restart loop: ``_run`` registers once with chubbyd then enters a loop that
spawns claude per iteration via ``_run_one_claude``. When the daemon pushes
a ``restart_claude`` event (triggered by the TUI's chubby-side
``/refresh-claude`` command), the loop SIGTERMs the running claude, waits
for PTY EOF, and re-launches with ``claude --resume <previous-session-id>``
so the conversation continues from the same JSONL file.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import collections
import json
import os
import re
import shutil
import signal
import struct
import sys
import time
from pathlib import Path
from typing import Any

from chubby.daemon import paths
from chubby.proto.errors import ChubError
from chubby.proto.rpc import Event
from chubby.wrapper.client import WrapperClient
from chubby.wrapper.pty import PtySession

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
# ``CHUBBY_AUTO_DISMISS_SETTINGS_ERROR=1`` (legacy: ``CHUB_AUTO_DISMISS_SETTINGS_ERROR=1``).
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

# Auto-respawn guard. When claude exits (Ctrl+C×2, /exit), the wrapper
# relaunches with --resume so the session feels persistent. A broken
# claude binary would otherwise spin forever, so the guard counts
# *consecutive* fast exits within a window and gives up after the
# threshold. A run lasting longer than the reset window clears the
# counter; a user-initiated /refresh-claude also resets, since the
# restart breaks the "consecutive crash" chain.
_RESPAWN_RESET_WINDOW_S = 30.0
_RESPAWN_MAX_FAST_EXITS = 5
_RESPAWN_BACKOFF_S = 0.3
_ENV_NO_AUTO_RESPAWN = "CHUBBY_NO_AUTO_RESPAWN"


class RespawnGuard:
    """Tracks consecutive fast claude exits to stop a crash loop.

    A "fast" exit is one whose run lasted ``_RESPAWN_RESET_WINDOW_S`` or
    less. After ``_RESPAWN_MAX_FAST_EXITS`` fast exits in a row,
    ``is_crash_looping()`` returns True and the wrapper bails out
    rather than respawning forever.
    """

    def __init__(self) -> None:
        self._fast_exits = 0

    def record_exit(self, run_duration_s: float) -> None:
        """Record an exit. A long-enough run resets the counter; a
        short run increments it."""
        if run_duration_s > _RESPAWN_RESET_WINDOW_S:
            self._fast_exits = 0
        self._fast_exits += 1

    def reset(self) -> None:
        """Clear the counter. Used when a user-initiated restart
        breaks the consecutive-crash chain."""
        self._fast_exits = 0

    @property
    def fast_exit_count(self) -> int:
        return self._fast_exits

    def is_crash_looping(self) -> bool:
        return self._fast_exits >= _RESPAWN_MAX_FAST_EXITS


# Where claude writes its per-session transcript JSONLs. Mirrors
# chubby.daemon.hooks.claude_projects_root() — duplicated here to keep
# the wrapper free of a daemon import for what is fundamentally a fact
# about claude's filesystem layout, not about the daemon.
def _claude_projects_root() -> Path:
    return Path.home() / ".claude" / "projects"


def _resume_is_safe(claude_session_id: str | None) -> bool:
    """True iff ``claude --resume <id>`` is expected to succeed.

    Claude refuses to resume a session whose JSONL doesn't exist or
    contains no user turns ("No conversation found with session ID:
    <id>"). The auto-respawn loop calls this before passing
    ``--resume`` so that a Ctrl+C'd-empty session relaunches as a
    fresh claude under the same chubby session, instead of crash-
    looping on a phantom resume id.

    Returns False if the id is None, the projects root is missing,
    no JSONL matches, or no record in the JSONL is a user turn.
    """
    if not claude_session_id:
        return False
    root = _claude_projects_root()
    if not root.is_dir():
        return False
    for jsonl in root.glob(f"*/{claude_session_id}.jsonl"):
        try:
            with jsonl.open("r", encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        rec = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if isinstance(rec, dict) and rec.get("type") == "user":
                        return True
        except OSError:
            continue
    return False

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
    (see ``spawn_session`` in chubby.daemon.main). These breadcrumbs let
    ``chubby diag <name>`` answer "why did this session die?" without us
    having to guess.
    """
    sys.stderr.write(f"[chubby-claude {time.strftime('%H:%M:%S')}] {msg}\n")
    sys.stderr.flush()


def parse_args(argv: list[str]) -> tuple[argparse.Namespace, list[str]]:
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("--name", default=paths.chubby_env("NAME"))
    # cwd default is "" (not os.getcwd()) so that ``chubby-claude --name x``
    # with no --cwd falls through to the HOME fallback in _run. The daemon
    # always passes a resolved --cwd; only direct invocations get here
    # with cwd="". (Treating an absent --cwd as "wherever I happen to be"
    # was a footgun: daemon-spawned wrappers inherit chubbyd's cwd, which
    # is rarely what the user means.)
    p.add_argument("--cwd", default="")
    p.add_argument("--tags", default="")
    # Phase 8d: when chubby spawns a wrapper to resume a historical
    # claude session, the daemon seeds this so iteration 1 launches
    # ``claude --resume <id>``. Subsequent auto-respawn iterations
    # use the wrapper's normal ``resume`` tracking (the resumed
    # session id), so we never end up with two --resume flags.
    p.add_argument("--initial-resume", default=None, dest="initial_resume")
    return p.parse_known_args(argv[1:])


def _exec_claude_directly(extra_argv: list[str]) -> None:
    claude = shutil.which("claude")
    if not claude:
        sys.exit("chubby-claude: 'claude' binary not found in PATH")
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
    Claude session id (None if we never saw one), a flag set when the
    inject-listener received a ``restart_claude`` event from the
    daemon, and a flag set when the daemon told us to ``shutdown`` (the
    ``/detach`` release path — we exit cleanly, no restart).
    """

    __slots__ = ("session_id", "restart_requested", "shutdown_requested")

    def __init__(self) -> None:
        self.session_id: str | None = None
        self.restart_requested: bool = False
        self.shutdown_requested: bool = False


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
    # Push a synthetic erase-display sequence to chubby's vt before the
    # new claude renders. The TUI's per-session vt grid is *reused*
    # across wrapper-internal claude restarts (the auto-respawn loop in
    # _run keeps the same wrapper, same daemon session id, same pty_chunk
    # broadcast subscriber → same vt grid). Claude's resume render only
    # does partial clears (line-erase [2K + cursor moves) and skips cells
    # that were filled by the prior claude — those stale cells leak
    # through as ghost text in the input box ("why is my last prompt /
    # the banner showing in the input?"). \x1b[2J erases all visible
    # cells; \x1b[3J also flushes scrollback; \x1b[H homes the cursor.
    # Harmless on the first iteration (fresh grid).
    try:
        seq += 1
        await client.push_chunk(seq=seq, data=b"\x1b[2J\x1b[3J\x1b[H")
    except ChubError:
        pass
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

        Disabled when ``CHUBBY_NO_AUTO_TRUST=1`` (legacy: ``CHUB_NO_AUTO_TRUST=1``) is set in the environment.
        """
        if paths.chubby_env("NO_AUTO_TRUST") == "1":
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
        ``CHUBBY_AUTO_DISMISS_SETTINGS_ERROR=1`` (legacy: ``CHUB_AUTO_DISMISS_SETTINGS_ERROR=1``)."""
        if paths.chubby_env("AUTO_DISMISS_SETTINGS_ERROR") != "1":
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
                # auto_newline (default True for back-compat) appends a
                # \r if the payload doesn't already end in one. The
                # legacy compose-bar path relies on this — typed text
                # arrives without a newline and the wrapper "submits"
                # it. Raw keystroke routing (single chars from the
                # embedded-PTY pane) MUST opt out: with auto_newline
                # the user's "k" becomes "k\r", which claude reads as
                # "submit the prompt 'k'", turning every character
                # into its own prompt.
                auto_newline = msg.params.get("auto_newline", True)
                if auto_newline and not payload.endswith(b"\n") and not payload.endswith(b"\r"):
                    payload += b"\r"
                await pty.write_user(payload)
            elif msg.method == "resize_pty":
                # Live PTY pane in chubby's TUI tells us its
                # conversation-pane dimensions; mirror them onto our
                # PTY so claude redraws to fit. The kernel SIGWINCHes
                # claude automatically once the size changes.
                try:
                    rows = int(msg.params.get("rows", 0))
                    cols = int(msg.params.get("cols", 0))
                except (TypeError, ValueError):
                    continue
                if rows > 0 and cols > 0:
                    try:
                        await pty.resize(rows, cols)
                    except Exception:
                        # Resize failures shouldn't kill the wrapper;
                        # claude will adapt at the next natural redraw.
                        pass
            elif msg.method == "restart_claude":
                _diag("restart_claude event received — SIGTERMing claude")
                result.restart_requested = True
                restart_requested.set()
                # SIGTERM the claude child; the PTY pump will see EOF and
                # return, which trips the gather() below.
                pty.signal_child(signal.SIGTERM)
                # Don't return — keep listening so a second restart event
                # during teardown still wins (idempotent: same effect).
            elif msg.method == "shutdown":
                # Daemon-side ``/detach`` release: SIGTERM claude and
                # let the main loop exit (no --resume retry). The user
                # is opening a fresh ``claude --resume <id>`` outside
                # chubby's management — we just need to disappear.
                _diag("shutdown event received — SIGTERMing claude (no restart)")
                result.shutdown_requested = True
                pty.signal_child(signal.SIGTERM)
            elif msg.method == "redraw_claude":
                # Daemon pushes this on every status flip to
                # AWAITING_USER. Claude's per-turn render uses
                # cursor-forward to skip cells it thinks already match
                # — fine in a real terminal (old content scrolls into
                # scrollback) but visible as ghost text in chubby's
                # bounded vt grid. A bare SIGWINCH isn't enough
                # because claude only re-lays-out on actual size
                # change. We toggle the size by ±1 col so claude
                # treats it as a real resize → recomputes layout from
                # scratch → rewrites every cell. The daemon already
                # pushed a synthetic erase-display chunk before this
                # event so claude's redraw lands on a clean canvas.
                _diag("redraw_claude received — toggling PTY size")
                try:
                    rows, cols = await pty.get_size()
                    await pty.resize(rows, max(cols + 1, 1))
                    # Tiny delay so claude observes both sizes as
                    # distinct events rather than coalescing them.
                    await asyncio.sleep(0.02)
                    await pty.resize(rows, cols)
                except Exception as e:
                    _diag(f"redraw_claude resize-toggle failed: {e!r}")

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
        sys.exit("chubby-claude: --name or CHUBBY_NAME required")

    # Tolerate an empty --cwd: fall back to $HOME, then ~, then "/" as a
    # last resort. The daemon resolves this up front for spawn_session
    # callers; this fallback is for direct ``chubby-claude --name x``
    # invocations (and as a defensive backstop).
    cwd = args.cwd or ""
    if not cwd:
        cwd = os.environ.get("HOME") or os.path.expanduser("~") or "/"

    _diag(f"starting: name={name} cwd={cwd} passthrough={passthrough}")

    sock = Path(paths.chubby_env("SOCK") or str(paths.sock_path()))
    client = WrapperClient(sock)
    try:
        await asyncio.wait_for(client._ensure(), timeout=2.0)
    except (FileNotFoundError, ConnectionRefusedError, TimeoutError):
        _diag(f"chubbyd not reachable on {sock}; exec'ing plain claude")
        sys.stderr.write(
            f"chubby-claude: chubbyd not running on {sock}; running plain claude\n"
        )
        _exec_claude_directly(passthrough)
        return 0

    tags = [t for t in args.tags.split(",") if t]
    # Phase 8d: ``--initial-resume <id>`` seeds the resume tracker so
    # the very first claude launch inside this wrapper is
    # ``claude --resume <id>``. Subsequent auto-respawn iterations
    # use the resumed session id naturally — no double-flag fight.
    resume: str | None = args.initial_resume or None
    is_first = True
    guard = RespawnGuard()
    try:
        while True:
            run_started = time.monotonic()
            res = await _run_one_claude(
                client=client,
                cwd=cwd,
                passthrough=passthrough,
                resume=resume,
                is_first_iteration=is_first,
                name=name,
                tags=tags,
            )
            is_first = False
            if res.shutdown_requested:
                # Daemon told us to release this session (``/detach``).
                # Exit cleanly — the user is taking the JSONL into a
                # fresh ``claude --resume`` outside our management.
                _diag("wrapper exiting: shutdown requested by daemon")
                return 0
            if res.session_id is not None:
                resume = res.session_id
            if res.restart_requested:
                # /refresh-claude path: user-initiated relaunch breaks
                # the consecutive-crash chain — clear the guard so a
                # later sequence of crashes is judged on its own.
                guard.reset()
                _diag(f"restarting claude with --resume {resume!r}")
                continue
            # Claude exited on its own (Ctrl+C×2, /exit, crash). Auto-
            # respawn so the wrapper survives. Two separate concerns:
            #   1. Keep the wrapper alive (so the chubby session entity
            #      doesn't disappear from the rail).
            #   2. Restore the conversation if possible — pass --resume
            #      only when claude can actually resume it. An empty
            #      JSONL would otherwise spin "No conversation found"
            #      until the crash-loop guard tripped.
            guard.record_exit(time.monotonic() - run_started)
            if guard.is_crash_looping():
                _diag(
                    f"auto-respawn: {_RESPAWN_MAX_FAST_EXITS}+ fast exits "
                    f"in {_RESPAWN_RESET_WINDOW_S:.0f}s, giving up"
                )
                return 0
            if os.environ.get(_ENV_NO_AUTO_RESPAWN):
                _diag(f"auto-respawn disabled via {_ENV_NO_AUTO_RESPAWN}")
                return 0
            if resume is not None and not _resume_is_safe(resume):
                _diag(
                    f"dropping --resume {resume!r}: JSONL has no user "
                    "turns; relaunching fresh"
                )
                resume = None
            _diag(
                f"auto-respawning claude with --resume {resume!r} "
                f"(attempt #{guard.fast_exit_count})"
            )
            await asyncio.sleep(_RESPAWN_BACKOFF_S)
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
            f"[chubby-claude] fatal: {type(e).__name__}: {e}\n"
            f"{traceback.format_exc()}\n"
        )
        sys.stderr.flush()
        raise


if __name__ == "__main__":
    main()
