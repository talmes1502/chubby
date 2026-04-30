from pathlib import Path

from chubby.daemon.logs import LogWriter


async def test_writes_chunks_with_color_header(tmp_path: Path) -> None:
    w = LogWriter(tmp_path / "logs", color="#5fafff", session_name="frontend")
    await w.append(b"hello world\n")
    await w.append(b"more output\n")
    await w.close()
    contents = (tmp_path / "logs" / "frontend.log").read_bytes()
    assert b"#5fafff" in contents  # color recorded in header
    assert b"hello world" in contents
    assert b"more output" in contents
