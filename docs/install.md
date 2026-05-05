# Installing chubby

## Requirements

- Python 3.12+
- `pipx` (recommended) or `uv`
- `claude` CLI (Anthropic Claude Code) on PATH
- macOS or Linux. Windows is not supported.
- For full attach to running sessions: `tmux` 3.0+

## Install

```bash
pipx install chubby-orchestrator
```

This gives you three commands: `chubby`, `chubbyd`, `chubby-claude`.

(The PyPI distribution name is `chubby-orchestrator` — the bare `chubby` name was already taken on PyPI by an unrelated package.)

Until the first PyPI release, install directly from the repo:

```bash
pipx install 'git+https://github.com/talmes1502/chubby.git'
```

The TUI is a separate Go binary downloaded automatically the first time you run `chubby tui`. To install it eagerly:

```bash
chubby tui --force-download
```

Or build from source:

```bash
git clone https://github.com/talmes1502/chubby
cd chubby/tui
go build -o ~/.local/bin/chubby-tui ./cmd/chubby-tui
```

## First-time setup

1. Start the daemon: `chubby up --detach`
2. Install the Stop hook (needed for awaiting-user notifications on chubby-launched sessions):
   ```bash
   chubby install-hooks --dry-run    # preview
   chubby install-hooks              # write
   ```
   Add `--auto-register` if you also want **every raw `claude` run anywhere on the machine** to auto-register as a readonly session in chubby's rail. Off by default — most people only want the rail to track sessions they explicitly spawned. You can flip it on/off any time by re-running `install-hooks`; chubby cleans up its own SessionStart entry when you downgrade.
3. (Recommended) Alias `claude` to `chubby-claude` so every Claude session is hub-aware:
   ```bash
   echo 'alias claude=chubby-claude' >> ~/.zshrc
   ```
   This is **not** automatic — auto-aliasing surprises people. Skip if you want only hand-launched sessions in the hub.
4. Run the TUI: `chubby tui`

## Common commands

```bash
# Sessions
chubby spawn --name backend --cwd ~/repo
chubby spawn --name web --cwd ~/repo --branch wip-login          # fresh worktree
chubby spawn --name review --cwd ~/repo --pr 42                  # from a GitHub PR
chubby list                                                       # pretty
chubby list --json                                                # machine-readable
chubby list --quiet | xargs -L1 chubby release                    # one id per line
chubby send backend "what are we working on?"
chubby broadcast --tag frontend "run tests"

# Release: full teardown (runs teardown scripts, removes worktree)
chubby release web                                                # one session
chubby release --tag frontend --yes                                 # all matching tag
chubby release --idle-since 2h --yes                              # idle longer than 2h (s/m/h/d)
chubby release --all --yes                                        # every live session

# History & search
chubby grep "RATE_LIMIT_EXCEEDED"
chubby history                          # past hub-runs (chubby-tracked)
# (cross-project history — every claude session ever — is in the TUI:
#  Shift+H from the rail)

# Attach existing claude processes
chubby attach --pick

# Spawn presets — saved templates
chubby preset create web --cwd ~/repo --branch "wip-{date}"
chubby preset apply web                 # spawns wip-2026-05-03
chubby preset list

# Diagnostics
chubby diag <name>                      # tail the wrapper's stderr file
```

### Per-project lifecycle scripts

Drop a `.chubby/config.json` in your repo root:

```json
{
  "setup":    ["./.chubby/setup.sh"],
  "teardown": ["./.chubby/teardown.sh"],
  "run":      ["bun dev"]
}
```

`setup` runs in a login shell (`zsh -lc`) before claude starts. Failure aborts the spawn. `teardown` runs on `chubby release` / `:detach`. The `run` array is reserved for future on-demand commands. Env vars exposed to the scripts:

- `CHUBBY_ROOT_PATH` — repo root
- `CHUBBY_WORKSPACE_NAME` — the chubby session name
- `CHUBBY_WORKSPACE_PATH` — the cwd the wrapper will spawn into (the worktree dir if `--branch` is set)

A gitignored `.chubby/config.local.json` can layer `before` / `after` arrays atop the committed `setup` / `teardown`.

### Per-project port labels

```json
// .chubby/ports.json
{ "3000": "web", "5432": "postgres" }
```

Annotates detected listening ports (chubby never creates ports — only labels what it finds). The rail badge `🌐 :3000` shows the label on hover.

### Editor handoff

```bash
export CHUBBY_EDITOR=pycharm
# (or `code`, `cursor`, `subl`, full path, etc.)
```

`Ctrl+X` from inside the editor pane (`Ctrl+O` to open) launches the file in your editor. If `$CHUBBY_EDITOR` is unset, chubby auto-detects `pycharm` / `code` / `cursor` / `subl` on `$PATH` — first hit wins.

### Inside the TUI

Press `?` for the full, scrollable, build-enforced reference of every key + chub-command + flag. Highlights:

- `Ctrl+P` — fuzzy session switcher (DEAD focus → respawn)
- `Ctrl+N` — new session modal (4 fields: name / cwd / branch / folder)
- `Shift+H` from rail — cross-project history browser (resume any historical claude session)
- `Ctrl+B` — broadcast a prompt to multiple sessions
- `Ctrl+H` — chubby's own hub-run history
- `:clone` / `:restart` / `:detach` / `:rename` — chub-commands via `:` rail palette

## Uninstall

```bash
pipx uninstall chubby
rm -rf ~/.claude/hub
# remove hook entries from ~/.claude/settings.json (look for "chubby-register-readonly" / "chubby-mark-idle")
```
