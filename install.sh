#!/usr/bin/env bash
# install.sh — one-shot installer for chubby.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/talmes1502/chubby/main/install.sh | bash
#
# Idempotent: re-running upgrades. Drops everything in ~/Apps/chubby
# (the source tree) and ~/.local/bin/chubby-tui (the Go binary).
# The Python parts go wherever pipx installs them (typically
# ~/.local/pipx/venvs/chubby-orchestrator).

set -euo pipefail

REPO_URL="https://github.com/talmes1502/chubby"
APPS_DIR="${HOME}/Apps"
SRC_DIR="${APPS_DIR}/chubby"
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

# --- prereqs ----------------------------------------------------------------
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

# Python ≥ 3.11 — pipx will use whatever python it was bootstrapped with.
PY_VER=$(python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')
PY_MAJOR=${PY_VER%%.*}
PY_MINOR=${PY_VER#*.}
if [[ "$PY_MAJOR" -lt 3 ]] || { [[ "$PY_MAJOR" -eq 3 ]] && [[ "$PY_MINOR" -lt 11 ]]; }; then
    red "✗ chubby needs Python 3.11+ (saw ${PY_VER})"
    red "  upgrade with: brew install python@3.12  (macOS) or your distro's package"
    exit 1
fi

# --- source tree ------------------------------------------------------------
mkdir -p "${APPS_DIR}"
if [[ -d "${SRC_DIR}/.git" ]]; then
    blue "↻ updating ${SRC_DIR}"
    git -C "${SRC_DIR}" fetch --quiet origin main
    git -C "${SRC_DIR}" reset --hard --quiet origin/main
else
    blue "↓ cloning into ${SRC_DIR}"
    git clone --quiet "${REPO_URL}" "${SRC_DIR}"
fi

# --- python side: chubby / chubbyd / chubby-claude --------------------------
blue "▸ installing the Python CLI via pipx"
# `pipx reinstall` upgrades cleanly if the package is already there;
# `pipx install` is the first-time path. Try reinstall first, fall
# through to install on a fresh machine.
if pipx list --short 2>/dev/null | grep -q '^chubby-orchestrator '; then
    pipx reinstall chubby-orchestrator >/dev/null
else
    pipx install --quiet "${SRC_DIR}"
fi

# --- go side: chubby-tui ---------------------------------------------------
blue "▸ building chubby-tui (Go)"
mkdir -p "${BIN_DIR}"
( cd "${SRC_DIR}/tui" && go build -ldflags "-s -w" -o "${BIN_DIR}/chubby-tui" ./cmd/chubby-tui )

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
green "  source:    ${SRC_DIR}"
green "  binary:    ${BIN_DIR}/chubby-tui"
green ""
green "  Run it:    chubby start"
