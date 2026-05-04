"""Detect listening TCP ports under a session's process tree.

The session's claude_pid is the root; the agent is likely to spawn
``bun dev`` / ``vite`` / ``python -m http.server`` etc., so we scan
the whole process tree (claude → its descendants) and collect every
listening port.

Implementation cribbed from Superset's ``packages/port-scanner`` (TS):
- macOS: ``lsof -p <pid_csv> -iTCP -sTCP:LISTEN -P -n`` (5 s timeout,
  tolerant of non-zero exit because lsof returns 1 when the PID set
  is empty).
- Linux: walk ``/proc/<pid>/net/tcp`` for hex-encoded local addresses
  with state ``0A`` (LISTEN). Returns the inode → resolve which PID
  in the tree owns it.

Ignored ports (well-known system services nobody starts as a dev
server): ``{22, 80, 443, 5432, 3306, 6379, 27017}`` — same set
Superset uses, so the same set of "this isn't useful UI noise"
applies.

Dedupe rule for the same port on multiple addresses (a server
listening on both ``127.0.0.1`` and ``::1`` shows twice in lsof):
prefer ``localhost > 0.0.0.0 > IPv6``.
"""

from __future__ import annotations

import asyncio
import logging
import re
import sys
from dataclasses import dataclass

log = logging.getLogger(__name__)


_LSOF_TIMEOUT_S = 5.0
_PGREP_TIMEOUT_S = 2.0
_PROCESS_TREE_MAX_DEPTH = 3

# Well-known service ports we never surface — they'd be noise from
# host services (sshd, nginx) or local databases that aren't part of
# the agent's dev workflow.
IGNORED_PORTS: frozenset[int] = frozenset({22, 80, 443, 5432, 3306, 6379, 27017})


@dataclass
class PortInfo:
    """One detected listening port."""
    port: int
    pid: int
    address: str  # "127.0.0.1" / "0.0.0.0" / "::1" / etc.


async def process_tree(root_pid: int) -> list[int]:
    """Return ``[root_pid, *descendants]`` up to a bounded depth.

    Uses ``pgrep -P`` recursively. Cap at depth 3 because deeper
    isn't typical (claude → bun → bun-runtime is depth 2; further
    nesting is rare and the cost adds up). Returns ``[root_pid]`` if
    pgrep isn't available.
    """
    visited: set[int] = {root_pid}
    frontier: list[int] = [root_pid]
    for _ in range(_PROCESS_TREE_MAX_DEPTH):
        if not frontier:
            break
        children = await _pgrep_children(frontier)
        new = [c for c in children if c not in visited]
        if not new:
            break
        visited.update(new)
        frontier = new
    return sorted(visited)


async def _pgrep_children(parents: list[int]) -> list[int]:
    """One ``pgrep -P p1,p2,...`` call. Returns the union of direct
    children. Empty list on any failure (best-effort)."""
    if not parents:
        return []
    try:
        proc = await asyncio.create_subprocess_exec(
            "pgrep", "-P", ",".join(str(p) for p in parents),
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
    except FileNotFoundError:
        return []
    try:
        stdout, _ = await asyncio.wait_for(
            proc.communicate(), timeout=_PGREP_TIMEOUT_S
        )
    except asyncio.TimeoutError:
        try:
            proc.kill()
        except ProcessLookupError:
            pass
        return []
    text = stdout.decode("utf-8", errors="replace")
    out: list[int] = []
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            out.append(int(line))
        except ValueError:
            continue
    return out


async def listening_ports(pids: list[int]) -> list[PortInfo]:
    """Return all listening TCP ports owned by any pid in ``pids``.

    macOS uses ``lsof``; Linux walks ``/proc``. Returns ``[]`` on any
    failure — the rail glyph is informational, never load-bearing.
    Filters ``IGNORED_PORTS`` and dedupes by ``(port, pid)`` keeping
    the canonical address per port.
    """
    if not pids:
        return []
    if sys.platform == "darwin":
        infos = await _listening_ports_lsof(pids)
    else:
        infos = await _listening_ports_proc(pids)
    infos = [i for i in infos if i.port not in IGNORED_PORTS]
    return _dedupe(infos)


async def _listening_ports_lsof(pids: list[int]) -> list[PortInfo]:
    """macOS: ``lsof -p <pid_csv> -iTCP -sTCP:LISTEN -P -n``. Validate
    PIDs against the requested set since lsof ignores ``-p`` when the
    PIDs don't exist (otherwise we'd report system-wide listening
    ports as belonging to our session)."""
    pid_csv = ",".join(str(p) for p in pids)
    requested = set(pids)
    try:
        proc = await asyncio.create_subprocess_exec(
            "lsof", "-p", pid_csv, "-iTCP", "-sTCP:LISTEN", "-P", "-n",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
    except FileNotFoundError:
        return []
    try:
        stdout, _ = await asyncio.wait_for(
            proc.communicate(), timeout=_LSOF_TIMEOUT_S
        )
    except asyncio.TimeoutError:
        try:
            proc.kill()
        except ProcessLookupError:
            pass
        return []
    text = stdout.decode("utf-8", errors="replace")
    return _parse_lsof_output(text, requested)


# Match ``host:port`` in lsof's NAME column. Hosts may be ``*`` (any
# interface), ``127.0.0.1``, ``[::1]``, ``localhost``, etc. Ports are
# numeric (the ``-P`` flag disables service-name conversion).
_LSOF_NAME_RE = re.compile(r"(\S+):(\d+)\s*\(LISTEN\)")


def _parse_lsof_output(text: str, requested_pids: set[int]) -> list[PortInfo]:
    """Parse the lsof column-formatted output. Skip the header and
    any rows whose pid isn't one we asked about."""
    out: list[PortInfo] = []
    for line in text.splitlines():
        line = line.rstrip()
        if not line or line.startswith("COMMAND"):
            continue
        # Columns: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
        # NAME may contain spaces ("(LISTEN)") so split on whitespace
        # but rejoin from column 8 onwards.
        cols = line.split(None, 8)
        if len(cols) < 9:
            continue
        try:
            pid = int(cols[1])
        except ValueError:
            continue
        if pid not in requested_pids:
            continue
        m = _LSOF_NAME_RE.search(cols[8])
        if m is None:
            continue
        host = m.group(1)
        try:
            port = int(m.group(2))
        except ValueError:
            continue
        # Normalize ``*`` to ``0.0.0.0`` (lsof's "any interface").
        if host == "*":
            host = "0.0.0.0"
        # ``[::1]`` → ``::1``
        if host.startswith("[") and host.endswith("]"):
            host = host[1:-1]
        out.append(PortInfo(port=port, pid=pid, address=host))
    return out


async def _listening_ports_proc(pids: list[int]) -> list[PortInfo]:
    """Linux: ``ss -ltnpH`` (no header, listening only, TCP, numeric,
    show owning process). Simpler than walking ``/proc`` and faster.
    """
    try:
        proc = await asyncio.create_subprocess_exec(
            "ss", "-ltnpH",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
    except FileNotFoundError:
        return []
    try:
        stdout, _ = await asyncio.wait_for(
            proc.communicate(), timeout=_LSOF_TIMEOUT_S
        )
    except asyncio.TimeoutError:
        try:
            proc.kill()
        except ProcessLookupError:
            pass
        return []
    text = stdout.decode("utf-8", errors="replace")
    requested = set(pids)
    return _parse_ss_output(text, requested)


# ``users:(("name",pid=NN,fd=NN))`` — extract every pid we see and
# match against the requested set.
_SS_PID_RE = re.compile(r"pid=(\d+)")
# Local-address column shape: ``127.0.0.1:3000`` / ``[::1]:3000`` /
# ``*:3000`` / ``0.0.0.0:3000``. Captures host + port.
_SS_LOCAL_RE = re.compile(r"^(\S+):(\d+)$")


def _parse_ss_output(text: str, requested_pids: set[int]) -> list[PortInfo]:
    out: list[PortInfo] = []
    for line in text.splitlines():
        cols = line.split()
        if len(cols) < 5:
            continue
        # cols: State Recv-Q Send-Q Local Peer [Process]
        local = cols[3]
        m = _SS_LOCAL_RE.match(local)
        if m is None:
            continue
        host = m.group(1)
        if host == "*":
            host = "0.0.0.0"
        if host.startswith("[") and host.endswith("]"):
            host = host[1:-1]
        try:
            port = int(m.group(2))
        except ValueError:
            continue
        process_col = cols[5] if len(cols) >= 6 else ""
        for pid_match in _SS_PID_RE.findall(process_col):
            try:
                pid = int(pid_match)
            except ValueError:
                continue
            if pid not in requested_pids:
                continue
            out.append(PortInfo(port=port, pid=pid, address=host))
    return out


def _address_rank(addr: str) -> int:
    """Lower is better. Used by ``_dedupe`` to pick the canonical
    address when one server listens on multiple bindings.

    Order: ``localhost`` / ``127.0.0.1`` < ``0.0.0.0`` < IPv6 < other.
    """
    if addr in ("127.0.0.1", "localhost"):
        return 0
    if addr == "0.0.0.0":
        return 1
    if addr.startswith("::") or ":" in addr:
        return 2
    return 3


def _dedupe(infos: list[PortInfo]) -> list[PortInfo]:
    """Keep one entry per ``(port, pid)`` pair, preferring the
    address with the lower rank. Sorted by port for stable output
    so the rail glyph order doesn't flip between scans."""
    by_key: dict[tuple[int, int], PortInfo] = {}
    for info in infos:
        key = (info.port, info.pid)
        existing = by_key.get(key)
        if existing is None or _address_rank(info.address) < _address_rank(existing.address):
            by_key[key] = info
    return sorted(by_key.values(), key=lambda i: (i.port, i.pid))
