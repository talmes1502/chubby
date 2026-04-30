#!/usr/bin/env bash
# scripts/release-tui.sh — build chub-tui for darwin+linux on amd64+arm64,
# upload to the GitHub release matching the current tag.
set -euo pipefail

VERSION="${1:?usage: $0 <version>}"

cd tui

mkdir -p ../dist
for os in darwin linux; do
    for arch in arm64 amd64; do
        out="../dist/chub-tui-${os}-${arch}"
        echo "building $out"
        GOOS=$os GOARCH=$arch go build -ldflags="-s -w" -o "$out" ./cmd/chub-tui
    done
done

cd ..
gh release upload "v${VERSION}" dist/chub-tui-* --clobber
