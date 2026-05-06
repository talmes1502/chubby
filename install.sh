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
RELEASE_API="https://api.github.com/repos/talmes1502/chubby/releases/latest"
BIN_DIR="${HOME}/.local/bin"

# Resolve the version to install. Order:
#   1. CHUBBY_VERSION=… in the env  → pin to that tag (e.g. for downgrades)
#   2. /releases/latest from GitHub  → tracks whatever's been published
# Without a fallback the script would refuse to run if the GitHub API
# is unreachable — that's worse for the user than letting them
# override with the env var.
if [[ -z "${CHUBBY_VERSION:-}" ]]; then
    CHUBBY_VERSION=$(
        curl -fsSL "${RELEASE_API}" 2>/dev/null \
            | sed -nE 's/.*"tag_name": *"v([^"]+)".*/\1/p' \
            | head -1
    )
fi
if [[ -z "${CHUBBY_VERSION:-}" ]]; then
    printf '\033[0;31m✗ could not resolve latest release tag from %s\033[0m\n' \
        "${RELEASE_API}" >&2
    printf '\033[0;31m  set CHUBBY_VERSION=x.y.z to install a specific version\033[0m\n' >&2
    exit 1
fi

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
# Always uninstall before install so a re-run is a clean upgrade.
# pipx upgrade is a no-op for git+URL installs (no version index to
# diff against), and `pipx install` refuses if the package is
# already there. The previous CHUBBY_FORCE=1 escape hatch didn't
# work via `CHUBBY_FORCE=1 curl ... | bash` because the env var
# doesn't propagate across the pipe — it attached to curl, not
# bash. Uninstall-then-install is idempotent and matches the user's
# mental model: "rerun install.sh and I'm on the latest."
blue "▸ installing Python CLI via pipx (from ${REPO_GIT_URL})"
pipx uninstall chubby-orchestrator >/dev/null 2>&1 || true
# Legacy install-name (ours used to be just "chubby" before the
# PyPI-collision rename). Sweep it too so two stale venvs don't
# linger.
pipx uninstall chubby >/dev/null 2>&1 || true
pipx install --quiet "git+${REPO_GIT_URL}"

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
