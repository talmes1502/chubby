# chubby

A local, terminal-native orchestrator for many parallel Claude Code sessions. Spawn dozens of agents on different branches of the same repo, watch them in a unified rail, drive them from a CLI that's friendly to outer agents, and resume any historical claude session — all from one TUI on your machine.

```
chubby · 4 sessions
┌─Sessions────────────────┐ ┌────────────────────────────────────────────────────┐
│ 📁 myrepo                │ │ ❯ ⏵⏵ tests are failing in src/auth                  │
│   web      ○  ↑2  🌐:3000│ │ ✻ Sautéed for 4s                                    │
│   api      ●  ↑1         │ │                                                      │
│   tests    ⚡ ↑3↓1        │ │ I found the failing test in src/auth/Login.tsx —    │
│ blabla     ○             │ │ the mock for `useUser()` is missing the …           │
│                          │ │                                                      │
│   :for chubby command    │ │                                                      │
└──────────────────────────┘ └────────────────────────────────────────────────────┘
Tab switch · Ctrl+\ cycle · Ctrl+P switch session · Ctrl+N new · ? help
```

Designed for the `git worktree` workflow: every session can opt into its own branch + worktree, run a project-defined `bun install` / `make deps` / etc. on spawn, and tear down cleanly on release.

## Why chubby?

- **Parallel by default.** Each session runs in its own git worktree; agents on different branches can edit the same repo without stepping on each other's working tree.
- **Fast to navigate.** `Ctrl+P` opens a fuzzy-search switcher across every live session by name, cwd, and (cached) opening prompt.
- **Per-project hooks.** Drop a `.chubby/config.json` with `setup` / `teardown` / `run` arrays and chubby runs them via your login shell at the right moments.
- **Useful glyphs.** Branch ahead/behind (`↑3↓1`), listening dev-server ports (`🌐 :3000`), and status (`○ ● ⚡ ✕`) all surface on the rail row.
- **Scriptable for outer agents.** Every CLI command auto-flips to JSON output when invoked under `CLAUDE_CODE` / `CODEX_CLI` / `GEMINI_CLI` / `CI`, so you can pipe `chubby list --quiet | xargs -L1 chubby release` from inside another agent without flag-juggling.
- **Resume anything.** A cross-project history browser scans `~/.claude/projects/*/*.jsonl` and lets you resume any historical session via `claude --resume` inside a fresh chubby wrapper.

## Install

See [docs/install.md](docs/install.md) for the full path. The short version:

```bash
pipx install chubby
chubby start                    # daemon + hooks + TUI in one command
```

Or do it piecewise:

```bash
chubby install-hooks            # Stop hook (add --auto-register to track every raw `claude` run)
chubby up --detach              # start the daemon
chubby tui                      # open the TUI
```

Requires macOS or Linux, Python 3.12+, the `claude` CLI on PATH, and `git` 2.20+.

## Feature tour

### Sessions, branches, worktrees

```bash
# fresh session at ~/myrepo on a new branch (creates the worktree)
chubby spawn --name web --cwd ~/myrepo --branch wip-login

# resume an existing branch
chubby spawn --name api --cwd ~/myrepo --branch feature/oauth

# spawn from a GitHub PR's head ref (best-effort; falls back without `gh`)
chubby spawn --name review --cwd ~/myrepo --pr 42
```

Worktrees live at `~/.claude/chubby/worktrees/<repo-hash>/<branch>` and are cleaned up on `chubby release` / `:detach` (uncommitted work survives a wrapper crash; only an explicit release removes the worktree).

### `.chubby/config.json` lifecycle scripts

Drop this in your repo root:

```json
{
  "setup":    ["./.chubby/setup.sh"],
  "teardown": ["./.chubby/teardown.sh"],
  "run":      ["bun dev"]
}
```

- `setup` runs in a login shell (`zsh -lc` / `bash -lc`) before claude starts. Failure aborts the spawn cleanly and rolls back the worktree.
- `teardown` runs on `:detach` / `chubby release`.
- Env vars exposed: `CHUBBY_ROOT_PATH`, `CHUBBY_WORKSPACE_NAME`, `CHUBBY_WORKSPACE_PATH`.

A gitignored `.chubby/config.local.json` can layer `before` / `after` arrays atop the committed config for personal extras (e.g., your `direnv allow` step).

### Branch ahead/behind in the rail

The daemon polls `git rev-list --left-right --count @{u}...HEAD` every 10 s for each non-DEAD session. Diverged branches show `↑3↓1`; in-sync branches show nothing.

### Port detection

Every 2.5 s the daemon walks each session's process tree and runs `lsof -iTCP -sTCP:LISTEN` (macOS) or `ss -ltnp` (Linux). New listeners get a `🌐 :3000` badge on the rail row, up to 2 ports plus a `+N` overflow. Well-known service ports (22 / 80 / 443 / 5432 / 3306 / 6379 / 27017) are filtered.

A `.chubby/ports.json` (e.g., `{"3000": "web", "5432": "postgres"}`) labels detected ports without creating them.

### Spawn presets

```bash
chubby preset create web --cwd ~/myrepo --branch "wip-{date}"
chubby preset apply web                    # spawns wip-2026-05-03
chubby preset apply web --name feature-x   # name override
chubby preset list
chubby preset delete web
```

Templates: `{date}` → today (`YYYY-MM-DD`), `{name}` → preset name. `~` is expanded at apply time so presets are portable across machines.

### Agent-context CLI output

Every CLI command auto-flips to JSON when invoked under any of: `CLAUDE_CODE`, `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `CODEX_CLI`, `GEMINI_CLI`, `CHUBBY_AGENT`, `CI`.

```bash
$ chubby list                           # human pretty
web ○ wrapped        /Users/tal/myrepo
api ● wrapped        /Users/tal/myrepo

$ CHUBBY_AGENT=1 chubby list            # auto-JSON for outer agents
[{"id":"s_01...","name":"web",...}]

$ chubby list --quiet | xargs -L1 chubby release   # one id per line
```

### Quick switcher

`Ctrl+P` from the rail opens a Cmd-P-style modal: substring match across name, cwd, and the cached first prompt of each session. ↑↓ navigate, Enter focus, Esc cancel.

(Special case: `Ctrl+P` on a DEAD session row respawns it instead — preserves the existing dead-row muscle memory.)

### Cross-project history browser

`Shift+H` from the rail (or `:` palette) opens a modal listing **every** claude session under `~/.claude/projects/*/*.jsonl` — chubby-tracked or not — sorted by recency, with a one-line first-prompt preview. Enter resumes the chosen session via `claude --resume <id>` in a fresh chubby wrapper.

### Editor hand-off

`Ctrl+O` opens a read-only file viewer (path prompt, `~` expansion, glamour rendering); `Ctrl+]` opens the most-recently-mentioned path from claude's output. Inside the editor pane, `Ctrl+X` opens the file in your real editor — resolution order:

1. `$CHUBBY_EDITOR` (any string command)
2. Auto-detect: `pycharm` / `code` / `cursor` / `subl` (first found on `$PATH`)

### Help

`?` from the rail opens the in-TUI help — scrollable, sectioned, every shipped feature documented with examples. The build is gated by a meta-test (`TestHelpBody_DocumentsEveryShippedFeature`) so a feature shipped without help-text fails CI.

## Common commands

```bash
chubby spawn --name X --cwd Y [--branch B | --pr N]
chubby list [--json | --quiet]
chubby send <name> "<prompt>"
chubby broadcast --tag <t> "<prompt>"
chubby grep "<query>"
chubby history                            # past hub-runs
chubby attach --pick                      # adopt running claude sessions
chubby release <name>                     # full teardown (runs teardown scripts)
chubby release --tag frontend --yes       # bulk: by tag
chubby release --idle-since 2h --yes      # bulk: anything idle >2h (s/m/h/d)
chubby release --all --yes                # bulk: every live session
chubby preset create|apply|list|delete|show
chubby diag <name>                        # tail the wrapper's stderr
```

## Architecture (for contributors)

```
            CLI (chubby) ──┐
            wrapper ───────┼─ Unix socket ─→ chubbyd (Python asyncio)
            TUI (chubby-tui, Go/Bubble Tea) ─┘            │
                                                          │
                                                  state.db, folders.json,
                                                  presets.json, runs/<id>/...
```

- **chubbyd** (`src/chubby/daemon/`) — owns the registry of live sessions, broadcasts events to TUI subscribers, runs background sweeps (stuck-thinking, git-status, port-scan), persists session metadata to SQLite.
- **chubby-claude** (`src/chubby/wrapper/`) — `pipx`-installed wrapper that spawns claude under a PTY, registers with the daemon, and proxies bytes both ways. Auto-respawns on claude exit (with `--resume`) until the crash-loop guard trips.
- **CLI** (`src/chubby/cli/`) — typer app; commands talk to the daemon over the same Unix socket. Output mode is global state set by the typer callback.
- **TUI** (`tui/`) — Bubble Tea + lipgloss. The conversation pane embeds `charmbracelet/x/vt` to render claude's UI live; the rail/compose/folders/quick-switcher are chubby's own layout.

## Documentation

- [docs/install.md](docs/install.md) — install, hooks, first-time setup
- [docs/smoke-test.md](docs/smoke-test.md) — manual pre-release check
- In-TUI: press `?` for the live, build-enforced reference.

## License

Private; not yet open-sourced.
