# Installing chub

## Requirements

- Python 3.12+
- `pipx` (recommended) or `uv`
- `claude` CLI (Anthropic Claude Code) on PATH
- macOS or Linux. Windows is not supported.
- For full attach to running sessions: `tmux` 3.0+

## Install

```bash
pipx install chub
```

This gives you three commands: `chub`, `chubd`, `chub-claude`.

The TUI is a separate Go binary downloaded automatically the first time you run `chub tui`. To install it eagerly:

```bash
chub tui --force-download
```

Or build from source:

```bash
git clone https://github.com/USER/chub
cd chub/tui
go build -o ~/.local/bin/chub-tui ./cmd/chub-tui
```

## First-time setup

1. Start the daemon: `chub up --detach`
2. Install hooks for read-only attach to raw `claude` sessions:
   ```bash
   chub install-hooks --dry-run    # preview
   chub install-hooks              # write
   ```
3. (Recommended) Alias `claude` to `chub-claude` so every Claude session is hub-aware:
   ```bash
   echo 'alias claude=chub-claude' >> ~/.zshrc
   ```
   This is **not** automatic — auto-aliasing surprises people. Skip if you want only hand-launched sessions in the hub.
4. Run the TUI: `chub tui`

## Common commands

```
chub spawn --name backend --cwd ~/repo
chub list
chub send backend "what are we working on?"
chub broadcast --tag sentra "run tests"
chub grep "DELAYED_QUEUE_FULL"
chub history
chub attach --pick
```

## Uninstall

```bash
pipx uninstall chub
rm -rf ~/.claude/hub
# remove hook entries from ~/.claude/settings.json (look for "chub-register-readonly" / "chub-mark-idle")
```
