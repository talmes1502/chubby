# Installing chubby

## Requirements

- Python 3.12+
- `pipx` (recommended) or `uv`
- `claude` CLI (Anthropic Claude Code) on PATH
- macOS or Linux. Windows is not supported.
- For full attach to running sessions: `tmux` 3.0+

## Install

```bash
pipx install chubby
```

This gives you three commands: `chubby`, `chubbyd`, `chubby-claude`.

The TUI is a separate Go binary downloaded automatically the first time you run `chubby tui`. To install it eagerly:

```bash
chubby tui --force-download
```

Or build from source:

```bash
git clone https://github.com/USER/chubby
cd chubby/tui
go build -o ~/.local/bin/chubby-tui ./cmd/chubby-tui
```

## First-time setup

1. Start the daemon: `chubby up --detach`
2. Install hooks for read-only attach to raw `claude` sessions:
   ```bash
   chubby install-hooks --dry-run    # preview
   chubby install-hooks              # write
   ```
3. (Recommended) Alias `claude` to `chubby-claude` so every Claude session is hub-aware:
   ```bash
   echo 'alias claude=chubby-claude' >> ~/.zshrc
   ```
   This is **not** automatic — auto-aliasing surprises people. Skip if you want only hand-launched sessions in the hub.
4. Run the TUI: `chubby tui`

## Common commands

```
chubby spawn --name backend --cwd ~/repo
chubby list
chubby send backend "what are we working on?"
chubby broadcast --tag sentra "run tests"
chubby grep "DELAYED_QUEUE_FULL"
chubby history
chubby attach --pick
```

## Uninstall

```bash
pipx uninstall chubby
rm -rf ~/.claude/hub
# remove hook entries from ~/.claude/settings.json (look for "chubby-register-readonly" / "chubby-mark-idle")
```
