"""`chubbyd` — daemon entrypoint."""

from __future__ import annotations

import asyncio
import base64
import json
import logging
import os
import re
import signal
import sys
from pathlib import Path
from typing import Any

from chubby import __version__
from chubby.daemon import paths
from chubby.daemon.clock import now_ms
from chubby.daemon.handlers import CallContext, HandlerRegistry
from chubby.daemon.logs import LogWriter
from chubby.daemon.persistence import Database
from chubby.daemon.pidlock import PidLockBusy, _alive, acquire
from chubby.daemon.registry import Registry
from chubby.daemon.runs import HubRun, end_run, resolve_resume, start_run
from chubby.daemon.server import Server
from chubby.daemon.session import SessionKind, SessionStatus
from chubby.daemon.subscriptions import SubscriptionHub
from chubby.proto.errors import ChubError, ErrorCode
from chubby.proto.schema import (
    AttachExistingReadonlyParams,
    AttachExistingReadonlyResult,
    AttachTmuxParams,
    AttachTmuxResult,
    DetachSessionParams,
    GetHubRunParams,
    GetHubRunResult,
    GetSessionHistoryParams,
    GetPtyBufferParams,
    GetPtyBufferResult,
    InjectParams,
    ListHubRunsParams,
    ListHubRunsResult,
    ListSessionsParams,
    ListSessionsResult,
    MarkIdleParams,
    PromoteSessionParams,
    PurgeParams,
    PushOutputParams,
    ClaudeJsonlEntry,
    ListAllClaudeJsonlsParams,
    ListAllClaudeJsonlsResult,
    ListRunCommandsParams,
    ListRunCommandsResult,
    RecentCwdsParams,
    RecentCwdsResult,
    RecolorSessionParams,
    RefreshClaudeSessionParams,
    RegisterReadonlyParams,
    RegisterReadonlyResult,
    RegisterWrappedParams,
    RegisterWrappedResult,
    ReleaseSessionParams,
    ReleaseSessionResult,
    RenameSessionParams,
    ResizePtyParams,
    RunCommandInfo,
    ScanCandidatesParams,
    ScanCandidatesResult,
    SearchTranscriptsParams,
    SessionDict,
    SetHubRunNoteParams,
    SetSessionTagsParams,
    SpawnSessionParams,
    StartRunCommandParams,
    StartRunCommandResult,
    StopRunCommandParams,
    StopRunCommandResult,
    UpdateClaudePidParams,
)
from chubby.proto.rpc import Event, encode_message

log = logging.getLogger("chubbyd")

PROTOCOL_VERSION = 1


def _parse_ts_ms(raw: Any) -> int:
    """Parse a Claude JSONL timestamp. Returns 0 if unparseable."""
    if not isinstance(raw, str):
        return 0
    try:
        from datetime import datetime
        # Claude uses ISO 8601 with optional Z suffix.
        return int(datetime.fromisoformat(raw.replace("Z", "+00:00")).timestamp() * 1000)
    except (ValueError, TypeError):
        return 0


def _build_registry(
    reg: Registry, run: HubRun, db: Database, subs: SubscriptionHub
) -> HandlerRegistry:
    h = HandlerRegistry()
    background_tasks: set[asyncio.Task[None]] = set()
    # Long-running ``run`` commands are kept here for the daemon's lifetime.
    # release/detach calls stop_all_for_session before teardown so dev
    # servers come down with the session.
    from chubby.daemon.run_processes import RunProcessRegistry

    run_processes = RunProcessRegistry()

    async def ping(params: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        return {"echo": params.get("message"), "server_time_ms": now_ms()}

    async def version(params: dict[str, Any], ctx: CallContext) -> dict[str, Any]:
        return {"version": __version__, "protocol": PROTOCOL_VERSION}

    async def register_wrapped(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chubby.daemon import hooks as hooks_mod

        p = RegisterWrappedParams.model_validate(params)
        s = await reg.register(
            name=p.name, kind=SessionKind.WRAPPED, cwd=p.cwd, pid=p.pid, tags=p.tags, color=p.color
        )
        await reg.attach_wrapper(s.id, ctx.write)

        # When the wrapper's connection closes (daemon-side EOF, wrapper crash
        # before session_ended, etc.), mark the session DEAD and detach the
        # writer. Without this hook, future inject calls land on a zombie
        # entry that's still ``wrapped`` + ``idle`` and the user is stuck
        # with WRAPPER_UNREACHABLE on a session that looks alive.
        sid = s.id

        async def on_wrapper_disconnect() -> None:
            try:
                cur = await reg.get(sid)
            except ChubError:
                return
            if cur.status is not SessionStatus.DEAD:
                await reg.update_status(sid, SessionStatus.DEAD)
            await reg.detach_wrapper(sid)

        ctx.on_close(on_wrapper_disconnect)

        writer = LogWriter(run.dir / "logs", color=s.color, session_name=s.name)
        await reg.attach_log_writer(s.id, writer)
        # Wrapped sessions don't know their Claude session id at registration
        # time — Claude only writes its JSONL once it actually starts up.
        # Prefer the pid → sessionId mapping at ~/.claude/sessions/<pid>.json
        # when the wrapper supplied a claude_pid; that's a precise binding
        # immune to the mtime race that hits when two Claudes share a cwd.
        task = asyncio.create_task(
            hooks_mod.watch_for_transcript(reg, s, claude_pid=p.claude_pid)
        )
        background_tasks.add(task)
        task.add_done_callback(background_tasks.discard)
        return RegisterWrappedResult(session=SessionDict(**s.to_dict())).model_dump()

    async def list_sessions(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        ListSessionsParams.model_validate(params)
        sessions = [SessionDict(**s.to_dict()) for s in await reg.list_all()]
        return ListSessionsResult(sessions=sessions).model_dump()

    async def rename_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = RenameSessionParams.model_validate(params)
        await reg.rename(p.id, p.name)
        return {}

    async def recolor_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = RecolorSessionParams.model_validate(params)
        await reg.recolor(p.id, p.color)
        return {}

    async def push_output(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = PushOutputParams.model_validate(params)
        await reg.record_chunk(p.session_id, base64.b64decode(p.data_b64), role=p.role)
        return {}

    async def inject(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = InjectParams.model_validate(params)
        s = await reg.get(p.session_id)
        if s.kind is SessionKind.READONLY:
            raise ChubError(
                ErrorCode.INJECTION_NOT_SUPPORTED,
                "session is read-only; restart via chubby-claude",
            )
        if s.status is SessionStatus.DEAD:
            raise ChubError(
                ErrorCode.SESSION_DEAD,
                "session is dead; respawn it before injecting",
            )
        await reg.inject(p.session_id, base64.b64decode(p.payload_b64))
        # Mark the session thinking; the next assistant transcript_message
        # in _tail_jsonl flips it back to idle. READONLY sessions can't be
        # injected (they raise above), so this branch only fires for
        # WRAPPED/SPAWNED/TMUX_ATTACHED.
        await reg.update_status(p.session_id, SessionStatus.THINKING)
        return {}

    async def inject_raw(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Like inject, but: (a) does NOT flip the session into
        THINKING, and (b) does NOT auto-append \\r to the payload.

        Used by the TUI for per-keystroke forwarding from the embedded
        PTY pane — printables, arrows, scroll keys, etc. The compose-
        bar prompt-submit path keeps using plain inject (which both
        flips status and auto-newlines).
        """
        p = InjectParams.model_validate(params)
        s = await reg.get(p.session_id)
        if s.kind is SessionKind.READONLY:
            raise ChubError(
                ErrorCode.INJECTION_NOT_SUPPORTED,
                "session is read-only; restart via chubby-claude",
            )
        if s.status is SessionStatus.DEAD:
            raise ChubError(
                ErrorCode.SESSION_DEAD,
                "session is dead; respawn it before injecting",
            )
        await reg.inject(
            p.session_id,
            base64.b64decode(p.payload_b64),
            auto_newline=False,
        )
        return {}

    async def get_pty_buffer(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Return the recent PTY bytes for a session so a TUI that
        attaches mid-conversation can prime its vt emulator and
        reconstruct claude's current screen. Capped at 64 KB on the
        registry side; b64-encoded over the wire to keep arbitrary
        ANSI / NUL bytes JSON-safe."""
        p = GetPtyBufferParams.model_validate(params)
        # Verify the session exists; raises SESSION_NOT_FOUND otherwise.
        await reg.get(p.session_id)
        data = reg.get_pty_buffer(p.session_id)
        return GetPtyBufferResult(
            buffer_b64=base64.b64encode(data).decode("ascii"),
        ).model_dump()

    async def resize_pty(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Forward a TUI's view-frame resize back to the wrapper's PTY,
        so claude redraws to fit chubby's conversation pane."""
        p = ResizePtyParams.model_validate(params)
        s = await reg.get(p.session_id)
        if s.kind is SessionKind.READONLY:
            # Read-only sessions live outside chubby's wrapper; their
            # PTY size is owned by whatever terminal claude was
            # launched in. Silently no-op rather than erroring — the
            # TUI's resize fan-out shouldn't fail just because one of
            # its sessions is observed-only.
            return {}
        if s.status is SessionStatus.DEAD:
            return {}
        await reg.resize(p.session_id, rows=p.rows, cols=p.cols)
        return {}

    async def session_ended(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        sid = params["session_id"]
        await reg.update_status(sid, SessionStatus.DEAD)
        await reg.detach_wrapper(sid)
        return {}

    async def spawn_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from pydantic import ValidationError
        try:
            p = SpawnSessionParams.model_validate(params)
        except ValidationError as e:
            # Translate pydantic missing-field errors into a clean
            # INVALID_PAYLOAD instead of bubbling up as INTERNAL.
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD, f"invalid spawn params: {e}"
            ) from None
        # Sessions are organized around a project cwd (rail grouping,
        # JSONL location, hooks scope), so empty cwd has no consistent
        # meaning. The TUI modal pre-fills $HOME and the CLI defaults to
        # os.getcwd() — so this should only fire on sloppy scripted
        # invocations. Surface it loudly rather than silently default.
        cwd = p.cwd.strip()
        if not cwd:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "cwd is required (try '--cwd ~' for $HOME)",
            )
        # Belt-and-braces: expand ~ on the daemon side too. The CLI
        # already calls os.path.expanduser, but a config file (preset,
        # automation) or an MCP client might pass a literal ``~/...``
        # through unfiltered. Path("~/foo") doesn't expand on its own;
        # we have to call expanduser explicitly.
        cwd = os.path.expanduser(cwd)
        # Branch / PR mode → resolve a chubby-managed git worktree
        # and override cwd with that path. The wrapper is unchanged
        # — it just sees a different cwd, which is exactly what we
        # want (claude renders inside the worktree, edits stay
        # isolated). Best-effort: any error falls back to a clear
        # INVALID_PAYLOAD instead of silently spawning into the
        # original cwd, since "I asked for a branch and got the
        # wrong tree" would corrupt the user's mental model.
        worktree_path: str | None = None
        repo_root_for_config: str | None = None
        if p.branch is not None or p.pr is not None:
            from chubby.daemon import worktree as _wt

            root = await _wt.repo_root(cwd)
            if root is None:
                raise ChubError(
                    ErrorCode.INVALID_PAYLOAD,
                    f"--branch/--pr requires a git repo; cwd={cwd!r} isn't one",
                )
            repo_root_for_config = str(root)
            branch_name = p.branch
            if p.pr is not None:
                resolved = await _wt.resolve_pr_branch(root, p.pr)
                if resolved is None:
                    raise ChubError(
                        ErrorCode.INVALID_PAYLOAD,
                        f"could not resolve PR #{p.pr} via gh — is gh "
                        f"installed and authed? (try `gh auth status`)",
                    )
                branch_name = resolved
            assert branch_name is not None
            try:
                wt_path = await _wt.add_worktree(root, branch_name)
            except _wt.WorktreeError as e:
                raise ChubError(ErrorCode.INVALID_PAYLOAD, str(e)) from None
            worktree_path = str(wt_path)
            cwd = worktree_path
        # Resolve project lifecycle config (Phase 2). For non-worktree
        # spawns, we still try to detect a git root above the cwd so
        # ``.chubby/config.json`` works even without ``--branch``. For
        # cwds outside any repo, repo_root falls back to the cwd
        # itself (load_config tolerates a missing file gracefully).
        if repo_root_for_config is None:
            from chubby.daemon import worktree as _wt
            r = await _wt.repo_root(cwd)
            repo_root_for_config = str(r) if r is not None else cwd
        from chubby.daemon import lifecycle_scripts as _lifecycle
        from chubby.daemon import project_config as _pc
        project_cfg = _pc.load_config(
            Path(cwd), Path(repo_root_for_config),
        )
        if project_cfg.setup:
            setup_env = {
                "CHUBBY_NAME": p.name,
                "CHUBBY_HOME": str(paths.hub_home()),
                "CHUBBY_ROOT_PATH": repo_root_for_config,
                "CHUBBY_WORKSPACE_NAME": p.name,
                "CHUBBY_WORKSPACE_PATH": cwd,
            }
            res = await _lifecycle.run_lifecycle(
                project_cfg.setup, cwd=Path(cwd), env=setup_env,
            )
            if res.status == "failed":
                # Roll back the worktree so a half-setup repo doesn't
                # leak — the user can fix their setup.sh and respawn
                # cleanly.
                if worktree_path is not None:
                    from chubby.daemon import worktree as _wt
                    try:
                        await _wt.remove_worktree(Path(worktree_path))
                    except Exception:  # pragma: no cover — defensive
                        pass
                raise ChubError(
                    ErrorCode.INVALID_PAYLOAD,
                    f"setup failed at {res.failed_command!r}: "
                    f"{res.output_tail.strip() or 'no output'}",
                )
        proc_env = {
            **os.environ,
            "CHUBBY_NAME": p.name,
            "CHUBBY_HOME": str(paths.hub_home()),
        }
        # Capture wrapper+claude stderr to a per-session file so we can later
        # inspect why a wrapper exited (claude died on first-run dialog,
        # auth failure, missing binary, etc.). Without this redirect we have
        # zero visibility — the wrapper inherits DEVNULL and silent exits
        # leave us guessing.
        wrappers_dir = run.dir / "wrappers"
        wrappers_dir.mkdir(parents=True, exist_ok=True)
        safe_name = re.sub(r"[^a-zA-Z0-9_-]", "_", p.name)
        stderr_path = wrappers_dir / f"{safe_name}.stderr"
        # Pass the file object directly: asyncio dups its fd into the child,
        # so we can close our handle as soon as the spawn returns and the
        # child still has its end. Append-mode so respawn doesn't truncate.
        stderr_fp = open(stderr_path, "ab")
        wrapper_argv = [
            sys.executable, "-m", "chubby.wrapper.main",
            "--name", p.name, "--cwd", cwd, "--tags", ",".join(p.tags),
        ]
        # Phase 8d: when resuming a historical claude session, seed
        # the wrapper's ``resume`` variable on iteration 1 so the
        # subsequent auto-respawn loop's --resume flag uses the
        # same id rather than fighting it. Dedicated flag (not
        # passthrough) avoids the "two --resume flags after the
        # first iteration" double-flag problem.
        if p.resume_claude_session_id:
            wrapper_argv += ["--initial-resume", p.resume_claude_session_id]
        try:
            await asyncio.create_subprocess_exec(
                *wrapper_argv,
                stdin=asyncio.subprocess.DEVNULL,
                stdout=stderr_fp,
                stderr=stderr_fp,
                env=proc_env,
                start_new_session=True,
            )
        finally:
            stderr_fp.close()
        for _ in range(50):
            try:
                s = await reg.get_by_name(p.name)
                # If we resolved a worktree for this spawn, remember
                # it on the session so release/detach can clean up.
                if worktree_path is not None and s.worktree_path != worktree_path:
                    await reg.set_worktree_path(s.id, worktree_path)
                    s = await reg.get(s.id)
                return {"session": SessionDict(**s.to_dict()).model_dump()}
            except ChubError:
                await asyncio.sleep(0.1)
        raise ChubError(ErrorCode.INTERNAL, "spawned wrapper did not register")

    async def search_transcripts(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = SearchTranscriptsParams.model_validate(params)
        hub_run = None if p.all_runs else (p.hub_run_id or run.id)
        rows = await db.search(
            p.query,
            hub_run_id=hub_run,
            session_id=p.session_id,
            limit=p.limit,
        )
        return {"matches": rows}

    async def get_session_history(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Read the bound JSONL transcript and return user/assistant turns
        so the TUI can seed its viewport on startup. Returns an empty list
        if no transcript is bound (yet) or no JSONL file is found."""
        from chubby.daemon import hooks as hooks_mod

        p = GetSessionHistoryParams.model_validate(params)
        s = await reg.get(p.session_id)
        if s.claude_session_id is None:
            return {"turns": []}
        path = hooks_mod.find_jsonl_for_session(s.claude_session_id)
        if path is None:
            return {"turns": []}

        turns: list[dict[str, Any]] = []
        try:
            with open(path, encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        rec = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    t = rec.get("type")
                    if t not in ("user", "assistant"):
                        continue
                    msg = rec.get("message")
                    if t == "user":
                        # User records may carry only tool_result blocks
                        # (Claude's protocol delivers them via type=user).
                        # Splice any results onto the most recent matching
                        # tool_call in the history we've built so far,
                        # then skip if the record has no other prose.
                        results = hooks_mod._extract_tool_results(msg)
                        if results:
                            for tid, payload in results.items():
                                for prior in reversed(turns):
                                    spliced = False
                                    for tc in prior.get("tool_calls", []):
                                        if tc.get("id") == tid:
                                            tc["result_preview"] = payload["preview"]
                                            tc["result_is_error"] = payload["is_error"]
                                            spliced = True
                                            break
                                    if spliced:
                                        break
                            # If the record had ONLY tool_results (no
                            # prose content as a string), don't add a
                            # blank user turn.
                            content = msg.get("content") if isinstance(msg, dict) else None
                            if not isinstance(content, str):
                                continue
                    text, tool_calls = hooks_mod._extract_turn_payload(msg)
                    if not text and not tool_calls:
                        continue
                    ts_raw = rec.get("timestamp")
                    ts_ms = _parse_ts_ms(ts_raw) if ts_raw else 0
                    turns.append({
                        "role": "user" if t == "user" else "assistant",
                        "text": text,
                        "tool_calls": tool_calls,
                        "ts": ts_ms,
                    })
        except OSError:
            return {"turns": []}

        if p.limit > 0 and len(turns) > p.limit:
            turns = turns[-p.limit:]
        return {"turns": turns}

    async def register_readonly(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = RegisterReadonlyParams.model_validate(params)
        name = p.name or f"{os.path.basename(p.cwd.rstrip('/'))}-{p.claude_session_id[:4]}"
        s = await reg.register(
            name=name,
            kind=SessionKind.READONLY,
            cwd=p.cwd,
            claude_session_id=p.claude_session_id,
            tags=p.tags,
        )
        # Start the JSONL tailer for this session as a background task.
        # Imported lazily so tests can monkeypatch ``hooks.start_tailer``.
        from chubby.daemon import hooks as hooks_mod

        task = asyncio.create_task(hooks_mod.start_tailer(reg, s))
        background_tasks.add(task)
        task.add_done_callback(background_tasks.discard)
        return RegisterReadonlyResult(session=SessionDict(**s.to_dict())).model_dump()

    async def mark_idle(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = MarkIdleParams.model_validate(params)
        for s in await reg.list_all():
            if s.claude_session_id == p.claude_session_id:
                await reg.update_status(s.id, SessionStatus.AWAITING_USER)
                return {}
        return {}

    async def list_hub_runs(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        ListHubRunsParams.model_validate(params)
        return ListHubRunsResult(runs=await db.list_hub_runs()).model_dump()

    async def get_hub_run(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = GetHubRunParams.model_validate(params)
        runs = [r for r in await db.list_hub_runs() if r["id"] == p.id]
        if not runs:
            raise ChubError(ErrorCode.SESSION_NOT_FOUND, f"no hub-run {p.id}")
        sessions = [s.to_dict() for s in await db.list_sessions(hub_run_id=p.id)]
        return GetHubRunResult(run=runs[0], sessions=sessions).model_dump()

    async def set_hub_run_note(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = SetHubRunNoteParams.model_validate(params)
        await db.set_run_note(p.id, p.note)
        return {}

    async def set_session_tags(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = SetSessionTagsParams.model_validate(params)
        await reg.set_tags(p.id, add=p.add, remove=p.remove)
        return {}

    async def scan_candidates(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chubby.daemon.attach.scanner import scan

        ScanCandidatesParams.model_validate(params)
        cs = await scan()
        live = await reg.list_all()
        known_pids = {s.pid for s in live if s.pid is not None}
        return ScanCandidatesResult(
            candidates=[
                {
                    "pid": c.pid,
                    "cwd": c.cwd,
                    "tmux_target": c.tmux_target,
                    "classification": c.classification,
                    "already_attached": c.pid in known_pids,
                }
                for c in cs
            ]
        ).model_dump()

    async def attach_tmux(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chubby.daemon.attach.tmux import watch_pane

        p = AttachTmuxParams.model_validate(params)
        s = await reg.register(
            name=p.name,
            kind=SessionKind.TMUX_ATTACHED,
            cwd=p.cwd,
            pid=p.pid,
            tmux_target=p.tmux_target,
            tags=p.tags,
        )
        writer = LogWriter(run.dir / "logs", color=s.color, session_name=s.name)
        await reg.attach_log_writer(s.id, writer)
        stop = asyncio.Event()
        reg._tmux_stops[s.id] = stop
        task = asyncio.create_task(watch_pane(reg, s.id, p.tmux_target, stop=stop))
        background_tasks.add(task)
        task.add_done_callback(background_tasks.discard)
        return AttachTmuxResult(session=SessionDict(**s.to_dict())).model_dump()

    async def attach_existing_readonly(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Register a running raw ``claude`` PID as a readonly session.

        JSONL discovery is encoding-free: we scan ``~/.claude/projects/*/*.jsonl``
        for the most recent file whose first records reference this cwd.
        If none is found we still register the session — the user can still
        see it in lists; we just won't have a transcript feed.
        """
        from chubby.daemon import hooks as hooks_mod

        p = AttachExistingReadonlyParams.model_validate(params)
        name = p.name or f"{os.path.basename(p.cwd.rstrip('/'))}-{p.pid}"

        found = hooks_mod.find_new_jsonl_for_cwd(p.cwd, since_ms=0)
        claude_session_id: str | None = found.stem if found is not None else None

        s = await reg.register(
            name=name,
            kind=SessionKind.READONLY,
            cwd=p.cwd,
            pid=p.pid,
            claude_session_id=claude_session_id,
        )
        if claude_session_id is not None:
            task = asyncio.create_task(hooks_mod.start_tailer(reg, s))
            background_tasks.add(task)
            task.add_done_callback(background_tasks.discard)
        return AttachExistingReadonlyResult(
            session=SessionDict(**s.to_dict())
        ).model_dump()

    async def _run_teardown_if_configured(session_id: str) -> None:
        """Run any project ``teardown`` scripts before we tear down
        the session. Failure is non-blocking (logged + best-effort
        — Superset's "force-delete to skip" pattern). Runs even for
        non-worktree sessions, since teardown is "stop the dev
        server" not "clean the worktree"."""
        try:
            s = await reg.get(session_id)
        except ChubError:
            return
        if not s.cwd:
            return
        from chubby.daemon import lifecycle_scripts as _lifecycle
        from chubby.daemon import project_config as _pc
        from chubby.daemon import worktree as _wt
        r = await _wt.repo_root(s.cwd)
        repo_root_path = str(r) if r is not None else s.cwd
        cfg = _pc.load_config(Path(s.cwd), Path(repo_root_path))
        if not cfg.teardown:
            return
        env = {
            "CHUBBY_NAME": s.name,
            "CHUBBY_HOME": str(paths.hub_home()),
            "CHUBBY_ROOT_PATH": repo_root_path,
            "CHUBBY_WORKSPACE_NAME": s.name,
            "CHUBBY_WORKSPACE_PATH": s.cwd,
        }
        res = await _lifecycle.run_lifecycle(
            cfg.teardown, cwd=Path(s.cwd), env=env,
        )
        if res.status == "failed":
            log.warning(
                "teardown for session %r failed at %r: %s",
                s.name, res.failed_command, res.output_tail.strip(),
            )

    async def _cleanup_worktree_if_owned(session_id: str) -> None:
        """If a session was spawned with ``--branch``/``--pr``, remove
        its chubby-managed git worktree. Wrapper disconnects (crash/
        SIGKILL) deliberately do NOT call this — the user might
        respawn the same name and we'd nuke their uncommitted work.
        Only an explicit release/detach triggers cleanup."""
        try:
            s = await reg.get(session_id)
        except ChubError:
            return
        if not s.worktree_path:
            return
        from pathlib import Path as _Path
        from chubby.daemon import worktree as _wt
        try:
            await _wt.remove_worktree(_Path(s.worktree_path))
        except Exception as e:  # pragma: no cover — defensive
            log.warning(
                "worktree cleanup failed for %s at %s: %s",
                s.name, s.worktree_path, e,
            )

    async def _load_project_config_for_session(
        session_id: str,
    ) -> tuple[Path, "object"] | None:
        """Resolve the project config for ``session_id``. Returns
        ``(workspace_path, ProjectConfig)`` or ``None`` if the session
        has no cwd (already torn down)."""
        try:
            s = await reg.get(session_id)
        except ChubError:
            return None
        if not s.cwd:
            return None
        from chubby.daemon import project_config as _pc
        from chubby.daemon import worktree as _wt
        r = await _wt.repo_root(s.cwd)
        repo_root_path = str(r) if r is not None else s.cwd
        cfg = _pc.load_config(Path(s.cwd), Path(repo_root_path))
        return Path(s.cwd), cfg

    async def list_run_commands(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = ListRunCommandsParams.model_validate(params)
        loaded = await _load_project_config_for_session(p.session_id)
        if loaded is None:
            return ListRunCommandsResult(commands=[]).model_dump()
        _, cfg = loaded
        running = {
            rp.index: rp for rp in run_processes.list_for_session(p.session_id)
        }
        out = []
        for i, cmd in enumerate(cfg.run):
            rp = running.get(i)
            out.append(
                RunCommandInfo(
                    index=i,
                    cmd=cmd,
                    running=rp is not None,
                    pid=rp.pid if rp else None,
                    log_path=str(rp.log_path) if rp else None,
                )
            )
        return ListRunCommandsResult(commands=out).model_dump()

    async def start_run_command(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = StartRunCommandParams.model_validate(params)
        loaded = await _load_project_config_for_session(p.session_id)
        if loaded is None:
            raise ChubError(
                ErrorCode.SESSION_NOT_FOUND,
                f"session {p.session_id!r} has no cwd",
            )
        cwd, cfg = loaded
        if not cfg.run:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "no `run` array configured in .chubby/config.json",
            )
        if p.index < 0 or p.index >= len(cfg.run):
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                f"run index {p.index} out of range "
                f"(0..{len(cfg.run) - 1})",
            )
        cmd = cfg.run[p.index]
        s = await reg.get(p.session_id)
        # Logs go alongside the wrapper's stderr file under the hub-run
        # dir so chubby diag can find them with the same conventions.
        log_dir = run.dir / "logs"
        log_path = log_dir / f"{s.name}-run-{p.index}.log"
        env = {
            "CHUBBY_NAME": s.name,
            "CHUBBY_HOME": str(paths.hub_home()),
            "CHUBBY_WORKSPACE_NAME": s.name,
            "CHUBBY_WORKSPACE_PATH": s.cwd,
        }
        try:
            meta = await run_processes.start(
                session_id=p.session_id,
                index=p.index,
                cmd=cmd,
                cwd=cwd,
                env=env,
                log_path=log_path,
                clock_ms=now_ms(),
            )
        except RuntimeError as e:
            raise ChubError(ErrorCode.INVALID_PAYLOAD, str(e))
        return StartRunCommandResult(
            pid=meta.pid, log_path=str(meta.log_path), cmd=meta.cmd
        ).model_dump()

    async def stop_run_command(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = StopRunCommandParams.model_validate(params)
        stopped = await run_processes.stop(p.session_id, p.index)
        return StopRunCommandResult(stopped=stopped).model_dump()

    async def detach_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = DetachSessionParams.model_validate(params)
        # Resolve session (raises SESSION_NOT_FOUND if missing).
        await reg.get(p.id)

        # Past validation every step is best-effort — same rationale
        # as release_session: never let a flaky cleanup step block the
        # session being marked DEAD or stop a follow-up call from
        # finishing the chain.
        async def _safe(label: str, coro: Any) -> None:
            try:
                await coro
            except Exception as e:  # pragma: no cover — defensive
                log.warning(
                    "detach_session %s: %s step failed: %s",
                    p.id, label, e,
                )

        await _safe("stop_run", run_processes.stop_all_for_session(p.id))
        await _safe("teardown", _run_teardown_if_configured(p.id))
        stop = reg._tmux_stops.get(p.id)
        if stop is not None:
            stop.set()
        await _safe("update_status", reg.update_status(p.id, SessionStatus.DEAD))
        await _safe("cleanup_worktree", _cleanup_worktree_if_owned(p.id))
        return {}

    async def release_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Release a session from chubby's management — the daemon side
        of the TUI's ``/detach`` command.

        Captures ``claude_session_id`` + ``cwd`` so the caller can
        re-open a real ``claude --resume <id>`` outside chubby, then
        SIGTERMs the wrapper (which kills its claude child), marks the
        session DEAD, and removes it from the in-memory registry. The
        on-disk JSONL transcript is untouched — the new external
        claude resumes the same conversation seamlessly.
        """
        p = ReleaseSessionParams.model_validate(params)
        s = await reg.get(p.id)
        if not s.claude_session_id:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "session has no bound claude session id yet — wait a moment and retry",
            )
        result = ReleaseSessionResult(
            claude_session_id=s.claude_session_id,
            cwd=s.cwd,
        )
        # Tell the wrapper to shut down. For WRAPPED/SPAWNED kinds we
        # push a ``shutdown`` event over the wrapper's writer; the
        # wrapper SIGTERMs its claude child and exits. For TMUX_ATTACHED
        # we just stop the watcher (we never spawned the claude). For
        # READONLY we have nothing to kill — we just drop our view.
        if s.kind in (SessionKind.WRAPPED, SessionKind.SPAWNED):
            write = reg._wrapper_writers.get(s.id)
            if write is not None:
                try:
                    await write(
                        encode_message(
                            Event(method="shutdown", params={"session_id": s.id})
                        )
                    )
                except Exception:
                    # The wrapper may already be gone; we still want to
                    # detach our local view. Don't surface this as an RPC
                    # error.
                    pass
        elif s.kind is SessionKind.TMUX_ATTACHED:
            stop = reg._tmux_stops.get(s.id)
            if stop is not None:
                stop.set()
        # Past this point the user has committed to releasing — every
        # remaining step is best-effort cleanup. If ANY of them raises
        # (e.g. ``subs.broadcast`` to a flaky subscriber, a teardown
        # script that errors), we MUST still remove the session from
        # the registry. Otherwise the rail keeps showing a dead row
        # the user can't get rid of without restarting the daemon.
        async def _safe(label: str, coro: Any) -> None:
            try:
                await coro
            except Exception as e:  # pragma: no cover — defensive
                log.warning(
                    "release_session %s: %s step failed: %s",
                    s.id, label, e,
                )

        # Stop any long-running ``run`` commands first so dev servers
        # come down with the session.
        await _safe("stop_run", run_processes.stop_all_for_session(s.id))
        # Teardown first while the worktree's still on disk and the
        # wrapper's still up — gives setup-mirroring scripts a chance
        # to stop dev servers etc.
        await _safe("teardown", _run_teardown_if_configured(s.id))
        # Mark dead + drop from in-memory registry. The SQLite row
        # stays so hub-run history still shows this session ran; it
        # just won't appear in list_sessions / the TUI rail.
        await _safe("update_status", reg.update_status(s.id, SessionStatus.DEAD))
        await _safe("detach_wrapper", reg.detach_wrapper(s.id))
        await _safe("cleanup_worktree", _cleanup_worktree_if_owned(s.id))
        await _safe("remove_session", reg.remove_session(s.id))
        return result.model_dump()

    async def promote_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        from chubby.daemon.attach.promote import relaunch_wrapper, wait_for_exit

        p = PromoteSessionParams.model_validate(params)
        s = await reg.get(p.id)
        if s.kind is not SessionKind.READONLY:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD, "session is not readonly"
            )
        if s.pid is None:
            await relaunch_wrapper(name=s.name, cwd=s.cwd, tags=list(s.tags))
            await reg.update_status(s.id, SessionStatus.DEAD)
            return {}
        if not await wait_for_exit(s.pid, timeout=600.0):
            raise ChubError(
                ErrorCode.INTERNAL, "timed out waiting for raw claude to exit"
            )
        await reg.update_status(s.id, SessionStatus.DEAD)
        await relaunch_wrapper(name=s.name, cwd=s.cwd, tags=list(s.tags))
        return {}

    async def purge(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        p = PurgeParams.model_validate(params)
        if p.run_id is None and p.session is None:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD, "specify run_id or session"
            )
        if p.run_id is not None:
            await db.conn.execute(
                "DELETE FROM transcript_fts WHERE hub_run_id = ?", (p.run_id,)
            )
            await db.conn.execute(
                "DELETE FROM sessions WHERE hub_run_id = ?", (p.run_id,)
            )
            await db.conn.execute(
                "DELETE FROM hub_runs WHERE id = ?", (p.run_id,)
            )
            await db.conn.commit()
        else:
            assert p.session is not None
            s = await reg.get_by_name(p.session)
            await db.conn.execute(
                "DELETE FROM transcript_fts WHERE session_id = ?", (s.id,)
            )
            await db.conn.execute(
                "DELETE FROM sessions WHERE id = ?", (s.id,)
            )
            await db.conn.commit()
            # Drop in-memory state too so list_sessions stops returning it.
            async with reg._lock:
                reg._by_id.pop(s.id, None)
                reg._wrapper_writers.pop(s.id, None)
                reg._writers.pop(s.id, None)
                reg._buffers.pop(s.id, None)
        return {}

    async def update_claude_pid(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Wrapper-side restart finished: refresh the daemon's view of the
        claude pid for an existing session and re-arm watch_for_transcript.

        The chubby session id is stable — same wrapper, same row, same
        connection. Only the claude pid moved. Re-watching against the
        new pid lets us re-bind the JSONL via ``~/.claude/sessions/<pid>.json``.
        With ``claude --resume <sid>`` the JSONL is the SAME file so the
        binding is effectively idempotent, but we re-run the watcher so
        the daemon's per-pid mapping is current.
        """
        from chubby.daemon import hooks as hooks_mod

        p = UpdateClaudePidParams.model_validate(params)
        s = await reg.get(p.session_id)
        # Mutate the in-memory pid; persistence is handled when the
        # transcript watcher rebinds (or via update_status next time).
        s.pid = p.claude_pid
        # Re-run the transcript watcher to rebind. The previous
        # claude_session_id is preserved on `s` so a quick lookup of
        # ~/.claude/sessions/<new_pid>.json will return the same UUID
        # (claude --resume keeps it).
        task = asyncio.create_task(
            hooks_mod.watch_for_transcript(reg, s, claude_pid=p.claude_pid)
        )
        background_tasks.add(task)
        task.add_done_callback(background_tasks.discard)
        return {}

    async def refresh_claude_session(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Push a ``restart_claude`` event over the wrapper's writer.

        The wrapper handles the actual SIGTERM + ``claude --resume <sid>``
        relaunch. We just bridge the TUI request to the wrapper; we don't
        wait for the wrapper to come back (that arrives later as an
        ``update_claude_pid`` call from the wrapper itself).
        """
        p = RefreshClaudeSessionParams.model_validate(params)
        s = await reg.get(p.id)
        if s.kind not in (SessionKind.WRAPPED, SessionKind.SPAWNED):
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "/refresh-claude only applies to wrapped or spawned sessions",
            )
        if s.claude_session_id is None:
            raise ChubError(
                ErrorCode.INVALID_PAYLOAD,
                "session has no bound claude session id yet; wait a moment and retry",
            )
        write = reg._wrapper_writers.get(p.id)
        if write is None:
            raise ChubError(
                ErrorCode.WRAPPER_UNREACHABLE, "wrapper not connected"
            )
        await write(
            encode_message(
                Event(
                    method="restart_claude",
                    params={"session_id": p.id},
                )
            )
        )
        return {}

    async def recent_cwds(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Return the most-recently-used cwds across all sessions.

        Used by the TUI spawn modal's Ctrl+P picker so the user can
        cycle through recent project dirs without retyping. Distinct,
        ordered most-recent-first.
        """
        p = RecentCwdsParams.model_validate(params)
        cwds = await db.recent_cwds(p.limit)
        return RecentCwdsResult(cwds=cwds).model_dump()

    async def list_all_claude_jsonls(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        """Phase 8d: scan ~/.claude/projects for every claude session
        on disk (chubby-tracked or not) and return one record per
        session sorted by recency. The TUI's cross-project history
        browser uses this to surface "every conversation I've ever
        had" so users can resume any of them via ``claude --resume``.
        """
        p = ListAllClaudeJsonlsParams.model_validate(params)
        # Pure stat + read; no daemon state involved. Run in a thread
        # so a slow disk doesn't block the event loop.
        entries_raw = await asyncio.to_thread(
            hooks_mod.list_all_claude_jsonls, limit=p.limit,
        )
        entries = [ClaudeJsonlEntry(**e) for e in entries_raw]
        return ListAllClaudeJsonlsResult(entries=entries).model_dump()

    async def subscribe_events(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        sub_id = await subs.subscribe(ctx.write)
        return {"subscription_id": sub_id}

    async def unsubscribe(
        params: dict[str, Any], ctx: CallContext
    ) -> dict[str, Any]:
        sid = int(params["subscription_id"])
        await subs.unsubscribe(sid)
        return {}

    h.register("subscribe_events", subscribe_events)
    h.register("unsubscribe", unsubscribe)
    h.register("ping", ping)
    h.register("version", version)
    h.register("register_wrapped", register_wrapped)
    h.register("list_sessions", list_sessions)
    h.register("rename_session", rename_session)
    h.register("recolor_session", recolor_session)
    h.register("push_output", push_output)
    h.register("inject", inject)
    h.register("inject_raw", inject_raw)
    h.register("resize_pty", resize_pty)
    h.register("get_pty_buffer", get_pty_buffer)
    h.register("session_ended", session_ended)
    h.register("spawn_session", spawn_session)
    h.register("search_transcripts", search_transcripts)
    h.register("get_session_history", get_session_history)
    h.register("register_readonly", register_readonly)
    h.register("mark_idle", mark_idle)
    h.register("list_hub_runs", list_hub_runs)
    h.register("get_hub_run", get_hub_run)
    h.register("set_hub_run_note", set_hub_run_note)
    h.register("set_session_tags", set_session_tags)
    h.register("scan_candidates", scan_candidates)
    h.register("attach_tmux", attach_tmux)
    h.register("attach_existing_readonly", attach_existing_readonly)
    h.register("detach_session", detach_session)
    h.register("release_session", release_session)
    h.register("promote_session", promote_session)
    h.register("purge", purge)
    h.register("update_claude_pid", update_claude_pid)
    h.register("refresh_claude_session", refresh_claude_session)
    h.register("recent_cwds", recent_cwds)
    h.register("list_all_claude_jsonls", list_all_claude_jsonls)
    h.register("list_run_commands", list_run_commands)
    h.register("start_run_command", start_run_command)
    h.register("stop_run_command", stop_run_command)
    return h


# Stuck-thinking sweep parameters. A session normally exits THINKING via
# the JSONL tailer's transcript_message → update_status(IDLE) path. If
# Claude crashes mid-generation, the JSONL stays silent and the session
# stays THINKING forever — the rail spinner spins endlessly and the
# banner shows "Thinking… (Xm Ys)" until manually cleared.
#
# 3 minutes is generous for genuine extended-thinking (opus-4.7 rarely
# stays in extended-thinking longer than ~90s, and tool chains hit the
# JSONL tailer between calls which resets last_activity_at) while
# catching crashed/orphaned generations quickly enough that the rail
# spinner doesn't spin for hours after a crash. Override via
# CHUBBY_THINKING_SWEEP_S / CHUBBY_THINKING_MAX_S.
_SWEEP_INTERVAL_DEFAULT_S = 30.0
_SWEEP_MAX_AGE_DEFAULT_S = 180.0


async def _sweep_stuck_thinking(
    reg: Registry,
    *,
    interval_s: float | None = None,
    max_age_s: float | None = None,
) -> None:
    """Periodically revert sessions stuck in THINKING for too long.

    Iterates the registry every ``interval_s`` seconds; any session in
    THINKING whose last_activity_at is older than ``max_age_s`` is
    flipped to IDLE (and broadcast as session_status_changed so the TUI
    sees the rail spinner stop).
    """
    interval = interval_s if interval_s is not None else float(
        paths.chubby_env("THINKING_SWEEP_S") or _SWEEP_INTERVAL_DEFAULT_S
    )
    max_age = max_age_s if max_age_s is not None else float(
        paths.chubby_env("THINKING_MAX_S") or _SWEEP_MAX_AGE_DEFAULT_S
    )
    while True:
        try:
            await asyncio.sleep(interval)
        except asyncio.CancelledError:
            return
        try:
            await _sweep_once(reg, max_age)
        except Exception as e:  # pragma: no cover — defensive
            log.warning("stuck-thinking sweep failed: %s", e)


# Git-status sweep — polls each non-DEAD session's cwd for ahead/
# behind commit counts and emits session_git_status_changed when the
# values change. Runs less aggressively than the thinking sweep
# because git rev-list against a remote-less branch is fast but not
# free, and the rail glyph is informational rather than load-bearing.
_GIT_STATUS_SWEEP_DEFAULT_S = 10.0


async def _sweep_git_status(
    reg: Registry, *, interval_s: float | None = None
) -> None:
    """Periodically refresh ``Session.git_ahead`` / ``git_behind`` for
    every non-DEAD session by shelling out to ``git rev-list``."""
    interval = interval_s if interval_s is not None else float(
        paths.chubby_env("GIT_STATUS_SWEEP_S") or _GIT_STATUS_SWEEP_DEFAULT_S
    )
    while True:
        try:
            await asyncio.sleep(interval)
        except asyncio.CancelledError:
            return
        try:
            await _sweep_git_status_once(reg)
        except Exception as e:  # pragma: no cover — defensive
            log.warning("git-status sweep failed: %s", e)


async def _sweep_git_status_once(reg: Registry) -> int:
    """One pass over the registry. Returns the number of sessions
    whose cached counts changed (used by tests)."""
    from chubby.daemon import git_status as _git_status

    changed = 0
    for s in await reg.list_all():
        if s.status is SessionStatus.DEAD:
            continue
        if not s.cwd:
            continue
        result = await _git_status.ahead_behind(s.cwd)
        if result is None:
            ahead, behind = None, None
        else:
            ahead, behind = result
        if await reg.set_git_status(s.id, ahead, behind):
            changed += 1
    return changed


# Port-scan sweep — walks each non-DEAD session's process tree and
# emits session_ports_changed when listening-port set changes. 2.5 s
# matches Superset's port-manager cadence; cheap on macOS (one lsof
# per session ≈ 5–10 ms) and on Linux (one ss invocation total).
_PORT_SWEEP_DEFAULT_S = 2.5


async def _sweep_ports(reg: Registry, *, interval_s: float | None = None) -> None:
    """Periodically refresh ``Session.ports`` for every non-DEAD
    session that has a ``claude_pid`` recorded."""
    interval = interval_s if interval_s is not None else float(
        paths.chubby_env("PORT_SWEEP_S") or _PORT_SWEEP_DEFAULT_S
    )
    while True:
        try:
            await asyncio.sleep(interval)
        except asyncio.CancelledError:
            return
        try:
            await _sweep_ports_once(reg)
        except Exception as e:  # pragma: no cover — defensive
            log.warning("port sweep failed: %s", e)


async def _sweep_ports_once(reg: Registry) -> int:
    """One pass over the registry. Returns the number of sessions
    whose port set changed (used by tests)."""
    from chubby.daemon import ports as _ports

    changed = 0
    for s in await reg.list_all():
        if s.status is SessionStatus.DEAD:
            continue
        # Walk the wrapper's process tree (the wrapper is parent of
        # claude, claude is parent of the dev-server). We use the
        # wrapper pid (``s.pid``) rather than claude_pid so a dev
        # server spawned outside claude (e.g. from a setup script)
        # is also covered.
        if s.pid is None:
            continue
        pids = await _ports.process_tree(int(s.pid))
        infos = await _ports.listening_ports(pids)
        port_dicts = [
            {"port": i.port, "pid": i.pid, "address": i.address}
            for i in infos
        ]
        if await reg.set_ports(s.id, port_dicts):
            changed += 1
    return changed


async def _sweep_once(reg: Registry, max_age_s: float) -> int:
    """Revert any session stuck in THINKING longer than ``max_age_s``.

    Returns the number of sessions reverted (used by tests). Separate
    from the loop so tests can drive a single iteration deterministically
    without monkeypatching asyncio.sleep.
    """
    cutoff = now_ms() - int(max_age_s * 1000)
    reverted = 0
    for s in await reg.list_all():
        if s.status is not SessionStatus.THINKING:
            continue
        if s.last_activity_at >= cutoff:
            continue
        try:
            await reg.update_status(s.id, SessionStatus.IDLE)
            reverted += 1
            log.warning(
                "auto-reverted stuck THINKING session %r (no activity for %ds)",
                s.name,
                int((now_ms() - s.last_activity_at) / 1000),
            )
        except Exception as e:  # pragma: no cover — defensive
            log.warning("failed to revert stuck session %r: %s", s.name, e)
    return reverted


async def serve(*, stop_event: asyncio.Event | None = None) -> None:
    paths.hub_home().mkdir(parents=True, exist_ok=True)
    pid_path = paths.pid_path()
    sock_path = paths.sock_path()
    stop_event = stop_event or asyncio.Event()

    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop_event.set)

    with acquire(pid_path):
        db = await Database.open(paths.state_db_path())
        resume_target = paths.chubby_env("RESUME")
        resumed_from: str | None = None
        if resume_target:
            resumed_from = await resolve_resume(db, resume_target)
        run = await start_run(db, resumed_from=resumed_from)
        try:
            subs = SubscriptionHub()
            registry = Registry(
                hub_run_id=run.id, db=db, event_log=run.event_log, subs=subs
            )
            if resumed_from:
                for prev in await db.list_sessions(hub_run_id=resumed_from):
                    if prev.kind is SessionKind.READONLY:
                        continue
                    pid_alive = prev.pid is not None and _alive(prev.pid)
                    await registry.register(
                        name=prev.name,
                        kind=prev.kind,
                        cwd=prev.cwd,
                        pid=prev.pid if pid_alive else None,
                        tags=prev.tags,
                        color=prev.color,
                    )
                    if not pid_alive:
                        s = await registry.get_by_name(prev.name)
                        await registry.update_status(s.id, SessionStatus.DEAD)
            handlers = _build_registry(registry, run, db, subs)
            server = Server(sock_path=sock_path, registry=handlers)
            await server.start()
            sweep_task = asyncio.create_task(_sweep_stuck_thinking(registry))
            git_sweep_task = asyncio.create_task(_sweep_git_status(registry))
            port_sweep_task = asyncio.create_task(_sweep_ports(registry))
            try:
                server_closed_task = asyncio.create_task(server.wait_closed())
                stop_task = asyncio.create_task(stop_event.wait())
                done, pending = await asyncio.wait(
                    {server_closed_task, stop_task},
                    return_when=asyncio.FIRST_COMPLETED,
                )
                for t in pending:
                    t.cancel()
                if server_closed_task in done and not stop_event.is_set():
                    log.warning(
                        "chubbyd: server closed unexpectedly; shutting down"
                    )
            finally:
                sweep_task.cancel()
                git_sweep_task.cancel()
                port_sweep_task.cancel()
                for t in (sweep_task, git_sweep_task, port_sweep_task):
                    try:
                        await t
                    except (asyncio.CancelledError, Exception):
                        pass
                await server.stop()
        finally:
            await end_run(db, run.id)
            await db.close()


def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
    )
    try:
        asyncio.run(serve())
    except PidLockBusy as e:
        print(f"chubbyd: {e}", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main()
