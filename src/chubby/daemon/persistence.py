"""SQLite persistence layer (aiosqlite). Schema is created at open()."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import aiosqlite

from chubby.daemon.clock import now_ms
from chubby.daemon.session import Session, SessionKind, SessionStatus

SCHEMA = """
CREATE TABLE IF NOT EXISTS hub_runs (
    id TEXT PRIMARY KEY,
    started_at INTEGER NOT NULL,
    ended_at INTEGER,
    hostname TEXT,
    resumed_from TEXT,
    notes TEXT
);

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    hub_run_id TEXT NOT NULL,
    name TEXT NOT NULL,
    color TEXT NOT NULL,
    kind TEXT NOT NULL,
    cwd TEXT NOT NULL,
    claude_session_id TEXT,
    pid INTEGER,
    tmux_target TEXT,
    tags TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_activity_at INTEGER NOT NULL,
    ended_at INTEGER,
    UNIQUE (hub_run_id, name)
);

CREATE INDEX IF NOT EXISTS idx_sessions_hub_run ON sessions(hub_run_id);

CREATE TABLE IF NOT EXISTS name_colors (
    name TEXT PRIMARY KEY,
    color TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS transcript_fts USING fts5(
    session_id UNINDEXED,
    hub_run_id UNINDEXED,
    ts UNINDEXED,
    role UNINDEXED,
    content,
    tokenize='porter unicode61'
);
"""


async def _run_migrations(conn: aiosqlite.Connection) -> None:
    """Idempotent in-place migrations for additive column changes.

    SQLite's ``CREATE TABLE IF NOT EXISTS`` won't add columns to an
    existing table, so each migration tries an ``ALTER TABLE ADD
    COLUMN`` and swallows the "duplicate column" error from already-
    upgraded databases. Keep migrations additive only — never drop or
    rename a column, since older daemons may still be running against
    the same file.
    """
    additions: list[tuple[str, str, str]] = [
        # (table, column_name, type+default suffix). Phase 1 worktree
        # path is per-session metadata so cleanup survives a daemon
        # restart.
        ("sessions", "worktree_path", "TEXT"),
        # Phase 8c: cached first user-turn for the rail / quick
        # switcher preview. Persisted so we don't have to re-scan
        # every JSONL on daemon startup.
        ("sessions", "first_user_message", "TEXT"),
    ]
    for table, col, type_suffix in additions:
        try:
            await conn.execute(f"ALTER TABLE {table} ADD COLUMN {col} {type_suffix}")
        except Exception as e:  # aiosqlite wraps OperationalError
            msg = str(e).lower()
            if "duplicate column" in msg:
                continue
            raise


def _quote_fts5(q: str) -> str:
    """Sanitize a user query for FTS5 MATCH.

    FTS5 reserves "/", ":", "(", ")", "*", "-", "+", AND, OR, NOT and a
    few other tokens. Free-form prose with any of these chars produces
    a syntax error. We wrap each whitespace-separated token in double-
    quotes to force literal-phrase matching; embedded " is doubled per
    FTS5 escape rule. An empty input becomes "" — FTS5 treats that as
    "no rows", which is the correct answer for a blank query.
    """
    parts = [f'"{token.replace(chr(34), chr(34) * 2)}"' for token in q.split()]
    return " ".join(parts)


class Database:
    def __init__(self, conn: aiosqlite.Connection) -> None:
        self.conn = conn

    @classmethod
    async def open(cls, path: Path) -> Database:
        path.parent.mkdir(parents=True, exist_ok=True)
        conn = await aiosqlite.connect(str(path))
        await conn.executescript(SCHEMA)
        await _run_migrations(conn)
        await conn.commit()
        return cls(conn)

    async def close(self) -> None:
        await self.conn.close()

    async def upsert_session(self, s: Session) -> None:
        # The schema has both PRIMARY KEY(id) AND UNIQUE(hub_run_id,
        # name). SQLite only allows ONE ON CONFLICT clause per UPSERT,
        # so the INSERT below targets `id` — the most common case
        # (re-saving an existing session). The (hub_run_id, name)
        # conflict fires when the in-memory session has a new id but
        # the DB still has a stale row with the same name under the
        # same run — typical when a wrapper re-registers (new in-mem
        # id) while a previous row from an earlier wrapper instance
        # is still on disk. Pre-delete that orphan so the INSERT
        # succeeds. Bug surfaced when /color failed with
        # "UNIQUE constraint failed: sessions.hub_run_id, sessions.name".
        await self.conn.execute(
            "DELETE FROM sessions WHERE hub_run_id=? AND name=? AND id != ?",
            (s.hub_run_id, s.name, s.id),
        )
        await self.conn.execute(
            """INSERT INTO sessions
                 (id, hub_run_id, name, color, kind, cwd, claude_session_id, pid,
                  tmux_target, tags, status, created_at, last_activity_at, ended_at,
                  worktree_path, first_user_message)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
               ON CONFLICT(id) DO UPDATE SET
                 name=excluded.name, color=excluded.color, kind=excluded.kind,
                 cwd=excluded.cwd, claude_session_id=excluded.claude_session_id,
                 pid=excluded.pid, tmux_target=excluded.tmux_target, tags=excluded.tags,
                 status=excluded.status, last_activity_at=excluded.last_activity_at,
                 ended_at=excluded.ended_at, worktree_path=excluded.worktree_path,
                 first_user_message=excluded.first_user_message""",
            (
                s.id,
                s.hub_run_id,
                s.name,
                s.color,
                s.kind.value,
                s.cwd,
                s.claude_session_id,
                s.pid,
                s.tmux_target,
                json.dumps(s.tags),
                s.status.value,
                s.created_at,
                s.last_activity_at,
                s.ended_at,
                s.worktree_path,
                s.first_user_message,
            ),
        )
        await self.conn.commit()

    async def list_sessions(self, *, hub_run_id: str | None = None) -> list[Session]:
        if hub_run_id is None:
            cur = await self.conn.execute("SELECT * FROM sessions ORDER BY created_at")
        else:
            cur = await self.conn.execute(
                "SELECT * FROM sessions WHERE hub_run_id=? ORDER BY created_at", (hub_run_id,)
            )
        rows = await cur.fetchall()
        assert cur.description is not None
        cols = [d[0] for d in cur.description]
        out = []
        for r in rows:
            d = dict(zip(cols, r, strict=False))
            d["kind"] = SessionKind(d["kind"])
            d["status"] = SessionStatus(d["status"])
            d["tags"] = json.loads(d["tags"])
            out.append(Session(**d))
        return out

    async def insert_hub_run(self, run_id: str, hostname: str, resumed_from: str | None) -> None:
        await self.conn.execute(
            "INSERT INTO hub_runs (id, started_at, hostname, resumed_from) VALUES (?, ?, ?, ?)",
            (run_id, now_ms(), hostname, resumed_from),
        )
        await self.conn.commit()

    async def end_hub_run(self, run_id: str) -> None:
        await self.conn.execute("UPDATE hub_runs SET ended_at=? WHERE id=?", (now_ms(), run_id))
        await self.conn.commit()

    async def list_hub_runs(self) -> list[dict[str, Any]]:
        cur = await self.conn.execute(
            "SELECT id, started_at, ended_at, hostname, resumed_from, notes FROM hub_runs "
            "ORDER BY started_at DESC"
        )
        assert cur.description is not None
        cols = [d[0] for d in cur.description]
        return [dict(zip(cols, r, strict=False)) for r in await cur.fetchall()]

    async def set_preferred_color(self, name: str, color: str) -> None:
        await self.conn.execute(
            "INSERT INTO name_colors (name, color, updated_at) VALUES (?, ?, ?) "
            "ON CONFLICT(name) DO UPDATE SET color=excluded.color, updated_at=excluded.updated_at",
            (name, color, now_ms()),
        )
        await self.conn.commit()

    async def get_preferred_color(self, name: str) -> str | None:
        cur = await self.conn.execute("SELECT color FROM name_colors WHERE name=?", (name,))
        row = await cur.fetchone()
        return row[0] if row else None

    async def set_run_note(self, run_id: str, note: str) -> None:
        await self.conn.execute("UPDATE hub_runs SET notes=? WHERE id=?", (note, run_id))
        await self.conn.commit()

    async def insert_message(
        self, *, session_id: str, hub_run_id: str, ts: int, role: str, content: str
    ) -> None:
        await self.conn.execute(
            "INSERT INTO transcript_fts (session_id, hub_run_id, ts, role, content) "
            "VALUES (?, ?, ?, ?, ?)",
            (session_id, hub_run_id, ts, role, content),
        )
        await self.conn.commit()

    async def recent_cwds(self, limit: int = 20) -> list[str]:
        """Return the most-recently-used cwds across all sessions, distinct,
        ordered by created_at DESC. Filters out empty/null cwds. Used by
        the TUI spawn modal's Ctrl+P recent-dir picker.

        SQLite has no straightforward DISTINCT-preserving-order in a
        single statement (DISTINCT + ORDER BY by a non-projected column
        breaks across versions), so we sort first via a window-style
        subquery: pick the max created_at per cwd, then order by that.
        """
        if limit <= 0:
            return []
        cur = await self.conn.execute(
            "SELECT cwd FROM ("
            "  SELECT cwd, MAX(created_at) AS last_used "
            "  FROM sessions "
            "  WHERE cwd IS NOT NULL AND cwd != '' "
            "  GROUP BY cwd"
            ") ORDER BY last_used DESC LIMIT ?",
            (limit,),
        )
        rows = await cur.fetchall()
        return [r[0] for r in rows]

    async def search(
        self,
        query: str,
        *,
        hub_run_id: str | None = None,
        session_id: str | None = None,
        limit: int = 200,
    ) -> list[dict[str, Any]]:
        # FTS5's MATCH grammar reserves "/", ":", "(", ")", "*", etc.
        # User queries are free-form prose — sanitize via _quote_fts5
        # so reserved chars become part of a literal phrase rather
        # than a syntax error. A blank query is itself an FTS5 syntax
        # error (the empty string isn't a valid MATCH expression), so
        # short-circuit to no rows here.
        match_expr = _quote_fts5(query)
        if not match_expr:
            return []
        params: list[Any] = [match_expr]
        sql = (
            "SELECT session_id, hub_run_id, ts, role, "
            "snippet(transcript_fts, 4, '[', ']', '...', 8) AS snippet "
            "FROM transcript_fts WHERE transcript_fts MATCH ?"
        )
        if hub_run_id is not None:
            sql += " AND hub_run_id = ?"
            params.append(hub_run_id)
        if session_id is not None:
            sql += " AND session_id = ?"
            params.append(session_id)
        sql += " ORDER BY ts DESC LIMIT ?"
        params.append(limit)
        cur = await self.conn.execute(sql, tuple(params))
        assert cur.description is not None
        cols = [d[0] for d in cur.description]
        return [dict(zip(cols, r, strict=False)) for r in await cur.fetchall()]
