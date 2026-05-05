#!/usr/bin/env bash
# install.sh — one-shot installer for chubby.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/talmes1502/chubby/main/install.sh | bash
#
# No source clone. Installs both halves directly from upstream:
#   - Python CLI:   pipx install 'git+https://…'  → ~/.local/pipx/venvs/
#   - Go TUI binary: go install github.com/talmes1502/chubby/tui/cmd/chubby-tui@latest
#                    →  $GOBIN (defaults to ~/go/bin); we pin to
#                       ~/.local/bin so the Python CLI's `chubby tui`
#                       finds it on $PATH.
#
# Both target dirs already exist on a machine that has pipx (the
# prereq below). No new directories are created on the user's box.
#
# Re-run = upgrade. Both pipx and `go install` refresh the latest tip.

set -euo pipefail

REPO_GIT_URL="https://github.com/talmes1502/chubby.git"
GO_PKG="github.com/talmes1502/chubby/tui/cmd/chubby-tui@latest"
BIN_DIR="${HOME}/.local/bin"

red()   { printf '\033[0;31m%s\033[0m\n' "$*" >&2; }
green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
blue()  { printf '\033[0;34m%s\033[0m\n' "$*"; }

require_cmd() {
    local cmd="$1" hint="$2"
    if ! command -v "$cmd" >/dev/null 2>&1; then
        red "✗ ${cmd} not found on PATH"
        red "  ${hint}"
        exit 1
    fi
}

# --- prereqs ---------------------------------------------------------------
case "$(uname -s)" in
    Darwin|Linux) ;;
    *)
        red "✗ chubby supports macOS and Linux only (saw $(uname -s))"
        exit 1
        ;;
esac

require_cmd git    "install via your package manager (brew install git / apt install git)"
require_cmd go     "install Go 1.22+ from https://go.dev/dl/  (or: brew install go)"
require_cmd pipx   "install pipx: brew install pipx  OR  python3 -m pip install --user pipx"
require_cmd claude "install Claude Code CLI from https://docs.claude.com/claude-code"

PY_VER=$(python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')
PY_MAJOR=${PY_VER%%.*}
PY_MINOR=${PY_VER#*.}
if [[ "$PY_MAJOR" -lt 3 ]] || { [[ "$PY_MAJOR" -eq 3 ]] && [[ "$PY_MINOR" -lt 11 ]]; }; then
    red "✗ chubby needs Python 3.11+ (saw ${PY_VER})"
    red "  upgrade with: brew install python@3.12  (macOS) or your distro's package"
    exit 1
fi

# --- python side: chubby / chubbyd / chubby-claude -------------------------
blue "▸ installing Python CLI via pipx (from ${REPO_GIT_URL})"
if pipx list --short 2>/dev/null | grep -q '^chubby-orchestrator '; then
    pipx upgrade --quiet chubby-orchestrator
else
    pipx install --quiet "git+${REPO_GIT_URL}"
fi

# --- go side: chubby-tui ---------------------------------------------------
blue "▸ installing chubby-tui via 'go install ${GO_PKG}'"
GOBIN="${BIN_DIR}" go install "${GO_PKG}"

# --- PATH check ------------------------------------------------------------
case ":${PATH}:" in
    *":${BIN_DIR}:"*) ;;
    *)
        red "⚠ ${BIN_DIR} is not on \$PATH"
        red "  add to your shell rc:  export PATH=\"\$HOME/.local/bin:\$PATH\""
        ;;
esac

green ""
green "✓ chubby installed"
green "  python CLI:  $(command -v chubby 2>/dev/null || echo "(restart shell, then: chubby)")"
green "  tui binary:  ${BIN_DIR}/chubby-tui"
green ""
green "  Run it:      chubby start"
