"""Unit tests for _quote_fts5 plus an integration test that confirms
FTS5 search no longer crashes on user queries containing reserved
characters (the bug that surfaced as
``rpc error -33099: internal error: fts5: syntax error near "/"``).
"""

from __future__ import annotations

from pathlib import Path

import pytest

from chubby.daemon.persistence import Database, _quote_fts5


@pytest.mark.parametrize(
    "raw, expected",
    [
        ("", ""),
        ("   ", ""),
        ("hello", '"hello"'),
        ("foo bar", '"foo" "bar"'),
        ("foo/bar", '"foo/bar"'),
        ("path/to/file.py", '"path/to/file.py"'),
        ("a:b (c)", '"a:b" "(c)"'),
        ("AND OR NOT", '"AND" "OR" "NOT"'),
        ("wildcard*", '"wildcard*"'),
        # Embedded double-quote: doubled per FTS5 phrase-escape rule.
        ('say "hi"', '"say" """hi"""'),
        # Tabs and newlines collapse just like spaces (str.split default).
        ("foo\tbar\nbaz", '"foo" "bar" "baz"'),
    ],
)
def test_quote_fts5(raw: str, expected: str) -> None:
    assert _quote_fts5(raw) == expected


async def test_search_with_reserved_chars_does_not_crash(tmp_path: Path) -> None:
    """Regression test for the FTS5 syntax-error bug. A query containing
    ``/`` (an FTS5 reserved char) must complete without raising — the
    result may be empty, but it must not be a SQL syntax error."""
    db = await Database.open(tmp_path / "s.db")
    try:
        await db.insert_message(
            session_id="s_1",
            hub_run_id="hr_1",
            ts=1,
            role="assistant",
            content="reading src/auth/Modal.tsx for context",
        )
        # Each of these would have raised pre-fix.
        for tricky in ["src/auth", "(foo)", "path:line", '"quoted"', "AND OR"]:
            rows = await db.search(tricky)
            assert isinstance(rows, list)
        # And the slash query actually finds the row, since the snippet
        # is wrapped as the literal phrase "src/auth".
        rows = await db.search("src/auth")
        assert len(rows) == 1
        assert rows[0]["session_id"] == "s_1"
    finally:
        await db.close()


async def test_search_blank_query_returns_no_rows(tmp_path: Path) -> None:
    """A whitespace-only query should yield zero rows (not crash)."""
    db = await Database.open(tmp_path / "s.db")
    try:
        await db.insert_message(
            session_id="s_1",
            hub_run_id="hr_1",
            ts=1,
            role="user",
            content="hello",
        )
        assert await db.search("") == []
        assert await db.search("   ") == []
    finally:
        await db.close()
