"""Scan running processes for ``claude`` candidates."""

from __future__ import annotations

import asyncio
import os
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


@dataclass
class Candidate:
    pid: int
    cwd: str
    tmux_target: str | None
    classification: str  # "tmux_full" | "promote_required"


async def scan() -> list[Candidate]:
    pids = await _enumerate_claude_pids()
    candidates: list[Candidate] = []
    tmux_panes = await _tmux_pane_pids()  # pid -> "session:window.pane"
    for pid in pids:
        cwd = await _process_cwd(pid)
        target = await _tmux_target_for(pid, tmux_panes)
        candidates.append(
            Candidate(
                pid=pid,
                cwd=cwd or "?",
                tmux_target=target,
                classification="tmux_full" if target else "promote_required",
            )
        )
    return candidates


async def _enumerate_claude_pids() -> list[int]:
    if sys.platform == "darwin":
        try:
            proc = await asyncio.create_subprocess_exec(
                "ps",
                "-A",
                "-o",
                "pid=,command=",
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.DEVNULL,
            )
        except FileNotFoundError:
            return []
        out, _ = await proc.communicate()
        pids: list[int] = []
        for raw_line in out.decode(errors="replace").splitlines():
            line = raw_line.strip()
            if not line:
                continue
            parts = line.split(maxsplit=1)
            if len(parts) < 2:
                continue
            pid_s, cmd = parts
            argv0 = Path(cmd.split()[0]).name
            if argv0 == "claude":
                try:
                    pids.append(int(pid_s))
                except ValueError:
                    continue
        return pids
    # Linux: walk /proc. These are synthetic-fs reads; treat as effectively
    # in-memory and accept the ASYNC240 lint exception.
    proc_root = Path("/proc")
    if not proc_root.exists():
        return []
    pids = []
    for entry in proc_root.iterdir():
        if not entry.name.isdigit():
            continue
        try:
            comm = (entry / "comm").read_text().strip()
        except (FileNotFoundError, PermissionError, OSError):
            continue
        if comm == "claude":
            pids.append(int(entry.name))
    return pids


async def _process_cwd(pid: int) -> str | None:
    if sys.platform == "linux":
        try:
            return os.readlink(f"/proc/{pid}/cwd")
        except OSError:
            return None
    # macOS: use lsof
    try:
        proc = await asyncio.create_subprocess_exec(
            "lsof",
            "-a",
            "-p",
            str(pid),
            "-d",
            "cwd",
            "-F",
            "n",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
    except FileNotFoundError:
        return None
    out, _ = await proc.communicate()
    for line in out.decode(errors="replace").splitlines():
        if line.startswith("n"):
            return line[1:]
    return None


async def _tmux_pane_pids() -> dict[int, str]:
    try:
        proc = await asyncio.create_subprocess_exec(
            "tmux",
            "list-panes",
            "-a",
            "-F",
            "#{pane_pid} #{session_name}:#{window_index}.#{pane_index}",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
    except FileNotFoundError:
        return {}
    out, _ = await proc.communicate()
    if proc.returncode != 0:
        return {}
    result: dict[int, str] = {}
    for line in out.decode().splitlines():
        parts = line.split(maxsplit=1)
        if len(parts) == 2:
            try:
                result[int(parts[0])] = parts[1]
            except ValueError:
                continue
    return result


async def _tmux_target_for(pid: int, pane_pids: dict[int, str]) -> str | None:
    """Walk parent chain; if any ancestor is a tmux pane PID, return its target."""
    cur = pid
    for _ in range(20):
        if cur in pane_pids:
            return pane_pids[cur]
        ppid = _parent_pid(cur)
        if ppid is None or ppid <= 1 or ppid == cur:
            return None
        cur = ppid
    return None


def _parent_pid(pid: int) -> int | None:
    if sys.platform == "linux":
        try:
            stat = Path(f"/proc/{pid}/stat").read_text()
            return int(stat.split()[3])
        except (FileNotFoundError, ValueError, OSError):
            return None
    try:
        out = subprocess.run(
            ["ps", "-o", "ppid=", "-p", str(pid)],
            capture_output=True,
            text=True,
            timeout=2,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    try:
        return int(out.stdout.strip()) if out.stdout.strip() else None
    except ValueError:
        return None
