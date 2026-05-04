"""Tests for ``run_lifecycle`` — the helper that runs project setup/
teardown/run scripts through a login shell with a timeout and an
output-tail capture on failure.
"""

from __future__ import annotations

from pathlib import Path

from chubby.daemon.lifecycle_scripts import run_lifecycle


async def test_empty_commands_returns_skipped(tmp_path: Path) -> None:
    """Caller can pass an empty list and branch on ``status == "skipped"``
    instead of having to special-case the no-config case."""
    res = await run_lifecycle([], cwd=tmp_path)
    assert res.status == "skipped"


async def test_successful_command(tmp_path: Path) -> None:
    """Plain successful command exits 0 and we get ``status="ok"``."""
    sentinel = tmp_path / "sentinel"
    res = await run_lifecycle(
        [f"touch {sentinel}"],
        cwd=tmp_path,
    )
    assert res.status == "ok"
    assert sentinel.exists()


async def test_command_runs_in_supplied_cwd(tmp_path: Path) -> None:
    """Setup scripts must run with ``cwd`` as the working dir so a
    relative ``./.chubby/setup.sh`` resolves correctly."""
    sub = tmp_path / "sub"
    sub.mkdir()
    res = await run_lifecycle(["pwd > pwdfile"], cwd=sub)
    assert res.status == "ok"
    pwd_text = (sub / "pwdfile").read_text().strip()
    assert pwd_text.startswith(str(sub.resolve())) or pwd_text.startswith(str(sub))


async def test_env_passed_through(tmp_path: Path) -> None:
    """Env vars supplied to ``run_lifecycle`` are visible to the
    spawned shell so chubby can hand the script CHUBBY_ROOT_PATH /
    CHUBBY_WORKSPACE_NAME / CHUBBY_WORKSPACE_PATH."""
    res = await run_lifecycle(
        ["echo $CHUBBY_FOO > envfile"],
        cwd=tmp_path,
        env={"CHUBBY_FOO": "bar"},
    )
    assert res.status == "ok"
    assert (tmp_path / "envfile").read_text().strip() == "bar"


async def test_failure_captures_output_tail(tmp_path: Path) -> None:
    """A non-zero exit code returns ``status="failed"`` with the
    failing command name + the tail of stdout/stderr (combined)."""
    res = await run_lifecycle(
        ["echo 'about to fail'; false"],
        cwd=tmp_path,
    )
    assert res.status == "failed"
    assert res.exit_code != 0
    assert res.failed_command == "echo 'about to fail'; false"
    assert "about to fail" in res.output_tail


async def test_first_failure_stops_subsequent_commands(tmp_path: Path) -> None:
    """Lifecycle commands run sequentially; once one fails the rest
    are skipped so we don't run a destructive ``rm -rf`` after a
    failed prereq check."""
    sentinel = tmp_path / "should-not-exist"
    res = await run_lifecycle(
        ["false", f"touch {sentinel}"],
        cwd=tmp_path,
    )
    assert res.status == "failed"
    assert not sentinel.exists()


async def test_output_tail_truncated_to_4kb(tmp_path: Path) -> None:
    """Long output is trimmed to the tail bytes so a verbose script
    can't flood logs/events."""
    # Print 8 KB of 'a' then exit 1 so we get the failure + tail.
    res = await run_lifecycle(
        ["python3 -c 'print(\"a\" * 8192)'; exit 1"],
        cwd=tmp_path,
        tail_bytes=4096,
    )
    assert res.status == "failed"
    # The tail is at most 4 KB plus shell overhead (a trailing
    # newline). Be generous on the upper bound.
    assert len(res.output_tail) <= 4200


async def test_timeout_kills_hung_script(tmp_path: Path) -> None:
    """A script that hangs past the timeout gets SIGHUP'd, then
    SIGKILL'd, and the result is ``status="failed"`` with
    ``timed_out=True``."""
    res = await run_lifecycle(
        ["sleep 30"],
        cwd=tmp_path,
        timeout_s=0.5,
    )
    assert res.status == "failed"
    assert res.timed_out is True
