"""Tests for the port scanner.

We exercise the parsers (which are pure) directly with sample
``lsof`` / ``ss`` outputs, then drive the integration via a real
``python -m http.server`` subprocess that binds an ephemeral port —
that proves the OS-specific path actually finds a real listener.
"""

from __future__ import annotations

import asyncio
import socket
import subprocess
import sys
import time

import pytest

from chubby.daemon.ports import (
    IGNORED_PORTS,
    PortInfo,
    _dedupe,
    _parse_lsof_output,
    _parse_ss_output,
    listening_ports,
    process_tree,
)


# ---- Pure parsers ---------------------------------------------------


def test_parse_lsof_extracts_port_and_address() -> None:
    """A typical lsof row for ``python -m http.server`` on macOS."""
    text = (
        "COMMAND   PID  USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME\n"
        "Python  12345  user    3u  IPv4 0xabcdef0123456789      0t0  TCP 127.0.0.1:3000 (LISTEN)\n"
    )
    infos = _parse_lsof_output(text, requested_pids={12345})
    assert infos == [PortInfo(port=3000, pid=12345, address="127.0.0.1")]


def test_parse_lsof_skips_unrequested_pids() -> None:
    """lsof can leak ports from PIDs we didn't ask about when our
    requested PIDs don't exist. The parser must filter to the
    requested set."""
    text = (
        "COMMAND   PID  USER   FD   TYPE  DEVICE SIZE/OFF NODE NAME\n"
        "sshd      111  root    4u  IPv6     ...        0  TCP *:22 (LISTEN)\n"
        "node      222  user    7u  IPv4     ...        0  TCP 127.0.0.1:3000 (LISTEN)\n"
    )
    infos = _parse_lsof_output(text, requested_pids={222})
    assert len(infos) == 1
    assert infos[0].pid == 222


def test_parse_lsof_normalizes_wildcard_and_ipv6() -> None:
    """``*:3000`` → ``0.0.0.0``; ``[::1]:3001`` → ``::1``."""
    text = (
        "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\n"
        "n       1   u    3u IPv4   x        0   TCP *:3000 (LISTEN)\n"
        "n       1   u    4u IPv6   x        0   TCP [::1]:3001 (LISTEN)\n"
    )
    infos = _parse_lsof_output(text, requested_pids={1})
    addrs = sorted((i.port, i.address) for i in infos)
    assert addrs == [(3000, "0.0.0.0"), (3001, "::1")]


def test_parse_ss_extracts_port_and_pid() -> None:
    """Linux ss output with ``-tlnpH``."""
    text = (
        'LISTEN 0      511          127.0.0.1:3000           0.0.0.0:* '
        'users:(("node",pid=4242,fd=23))\n'
    )
    infos = _parse_ss_output(text, requested_pids={4242})
    assert infos == [PortInfo(port=3000, pid=4242, address="127.0.0.1")]


def test_parse_ss_skips_unrequested_pids() -> None:
    text = (
        'LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=111,fd=3))\n'
        'LISTEN 0 511 127.0.0.1:3000 0.0.0.0:* users:(("node",pid=222,fd=23))\n'
    )
    infos = _parse_ss_output(text, requested_pids={222})
    assert len(infos) == 1
    assert infos[0].pid == 222


# ---- Dedupe ---------------------------------------------------------


def test_dedupe_prefers_localhost_over_wildcard() -> None:
    infos = [
        PortInfo(port=3000, pid=1, address="0.0.0.0"),
        PortInfo(port=3000, pid=1, address="127.0.0.1"),
    ]
    out = _dedupe(infos)
    assert out == [PortInfo(port=3000, pid=1, address="127.0.0.1")]


def test_dedupe_prefers_wildcard_over_ipv6() -> None:
    infos = [
        PortInfo(port=3000, pid=1, address="::1"),
        PortInfo(port=3000, pid=1, address="0.0.0.0"),
    ]
    out = _dedupe(infos)
    assert out[0].address == "0.0.0.0"


def test_dedupe_keeps_distinct_pids_for_same_port() -> None:
    """Two processes accidentally listening on the same port (rare —
    but technically distinct from a multi-bind dedupe)."""
    infos = [
        PortInfo(port=3000, pid=1, address="127.0.0.1"),
        PortInfo(port=3000, pid=2, address="127.0.0.1"),
    ]
    out = _dedupe(infos)
    assert {i.pid for i in out} == {1, 2}


# ---- Integration ----------------------------------------------------


async def test_listening_ports_finds_real_python_server() -> None:
    """Spawn a real ``python -m http.server 0`` listening on an
    ephemeral port; assert ``listening_ports`` finds it. ``0``
    asks the kernel to assign a free port — we read it from the
    process's first stdout line."""
    # Pick a random ephemeral port up front via a probe socket so we
    # don't have to parse python's stdout (which is also racy).
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    if port in IGNORED_PORTS:
        pytest.skip(f"ephemeral port {port} is in IGNORED_PORTS")
    proc = subprocess.Popen(
        [sys.executable, "-u", "-m", "http.server", str(port), "--bind", "127.0.0.1"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        # Give the server a moment to bind.
        for _ in range(50):
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                    break
            except OSError:
                time.sleep(0.05)
        else:
            pytest.skip(f"http.server didn't bind {port} in time")
        infos = await listening_ports([proc.pid])
        ports_seen = {i.port for i in infos}
        assert port in ports_seen, f"expected {port} in {ports_seen}"
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()


async def test_listening_ports_filters_ignored() -> None:
    """A pid set that contains nothing listening on a non-ignored
    port returns an empty list — IGNORED_PORTS are filtered out
    after the parse, before dedupe."""
    # Probe a clearly-no-process pid (unlikely to be listening on
    # anything we'd surface). Use 1 (init/launchd) to ensure we
    # don't return data — anything it lists (e.g. system services
    # on 80/443) should be filtered.
    infos = await listening_ports([1])
    for info in infos:
        assert info.port not in IGNORED_PORTS


async def test_process_tree_includes_root() -> None:
    """A pid with no children returns just itself."""
    pids = await process_tree(99999)  # almost certainly no children
    assert 99999 in pids
