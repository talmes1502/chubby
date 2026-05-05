#!/usr/bin/env bash
# install.sh — one-shot installer for chubby.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/talmes1502/chubby/main/install.sh | bash
#
# Installs both halves of chubby from prebuilt artifacts:
#   - Python CLI:     pipx install 'git+https://…'  → ~/.local/pipx/venvs/
#   - Go TUI binary:  curl + tar from GitHub release → ~/.local/bin/chubby-tui
#
# No Go toolchain required (we ship prebuilt binaries via GoReleaser).
# No source clone, no temp dir, no app dir created. Both target dirs
# already exist if the user has pipx installed (the only real prereq).
#
# Re-run = upgrade. pipx upgrade picks up the latest main; the binary
# always pulls v$CHUBBY_VERSION (tracks the python package's version).

set -euo pipefail

REPO_GIT_URL="https://github.com/talmes1502/chubby.git"
RELEASE_BASE="https://github.com/talmes1502/chubby/releases/download"
# Pin to a specific version so the prebuilt binary matches whatever
# the python wheel claims. Override via env if you need a non-default.
CHUBBY_VERSION="${CHUBBY_VERSION:-0.1.0}"
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
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *)
        red "✗ chubby supports macOS and Linux only (saw $(uname -s))"
        exit 1
        ;;
esac

case "$(uname -m)" in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64)  arch=amd64 ;;
    *)
        red "✗ unsupported CPU architecture: $(uname -m)"
        exit 1
        ;;
esac

require_cmd pipx   "install pipx: brew install pipx  OR  python3 -m pip install --user pipx"
require_cmd curl   "install via your package manager"
require_cmd tar    "should be preinstalled — what flavor of unix is this?"
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
# CHUBBY_FORCE=1 wipes the existing pipx venv + binary first so the
# install starts from a clean slate. Useful when a previous install
# left bad state (wrong package on PyPI, stale virtualenv, etc.).
if [[ "${CHUBBY_FORCE:-0}" == "1" ]]; then
    blue "▸ CHUBBY_FORCE=1 — uninstalling any existing chubby first"
    pipx uninstall chubby-orchestrator 2>/dev/null || true
    pipx uninstall chubby 2>/dev/null || true
    rm -f "${BIN_DIR}/chubby-tui"
fi

blue "▸ installing Python CLI via pipx (from ${REPO_GIT_URL})"
if pipx list --short 2>/dev/null | grep -q '^chubby-orchestrator '; then
    # `pipx upgrade` only refreshes from a versioned index — for a
    # git+URL install it's a no-op, so reinstall to actually pull
    # latest main. CHUBBY_FORCE=1 above already uninstalled, so this
    # branch is for "I just want the latest main" upgrades.
    pipx uninstall chubby-orchestrator >/dev/null 2>&1 || true
    pipx install --quiet "git+${REPO_GIT_URL}"
else
    pipx install --quiet "git+${REPO_GIT_URL}"
fi

# --- go side: prebuilt chubby-tui binary -----------------------------------
asset="chubby-tui_${CHUBBY_VERSION}_${os}_${arch}.tar.gz"
url="${RELEASE_BASE}/v${CHUBBY_VERSION}/${asset}"
blue "▸ downloading prebuilt chubby-tui from ${url}"

mkdir -p "${BIN_DIR}"
tmpdir=$(mktemp -d)
trap 'rm -rf "${tmpdir}"' EXIT

if ! curl -fsSL "${url}" -o "${tmpdir}/${asset}"; then
    red "✗ failed to download ${url}"
    red "  if you know what you're doing, rebuild from source:"
    red "    go install github.com/talmes1502/chubby/tui/cmd/chubby-tui@latest"
    exit 1
fi

# Verify against checksums.txt before extracting.
if curl -fsSL "${RELEASE_BASE}/v${CHUBBY_VERSION}/checksums.txt" -o "${tmpdir}/checksums.txt"; then
    expected=$(awk -v a="${asset}" '$2==a {print $1}' "${tmpdir}/checksums.txt")
    if [[ -n "${expected}" ]]; then
        actual=$(shasum -a 256 "${tmpdir}/${asset}" | awk '{print $1}')
        if [[ "${expected}" != "${actual}" ]]; then
            red "✗ sha256 mismatch for ${asset}"
            red "  expected ${expected}"
            red "  got      ${actual}"
            exit 1
        fi
    fi
fi

tar -xzf "${tmpdir}/${asset}" -C "${tmpdir}"
mv "${tmpdir}/chubby-tui" "${BIN_DIR}/chubby-tui"
chmod 0755 "${BIN_DIR}/chubby-tui"

# --- PATH check ------------------------------------------------------------
case ":${PATH}:" in
    *":${BIN_DIR}:"*) ;;
    *)
        red "⚠ ${BIN_DIR} is not on \$PATH"
        red "  add to your shell rc:  export PATH=\"\$HOME/.local/bin:\$PATH\""
        ;;
esac

green ""
green "✓ chubby installed (v${CHUBBY_VERSION})"
green "  python CLI:  $(command -v chubby 2>/dev/null || echo "(restart shell, then: chubby)")"
green "  tui binary:  ${BIN_DIR}/chubby-tui"
green ""
green "  Run it:      chubby start"
