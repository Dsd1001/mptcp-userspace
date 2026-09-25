#!/usr/bin/env bash
# Build only: no service deployment, kernel, proxy or firewall changes.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
GO=${MPTCP_GO:-go}
if [[ $GO == go && -f "$ROOT/macos/build/go-path" ]]; then GO=$(cat "$ROOT/macos/build/go-path"); fi
export GOTOOLCHAIN=local
export GOCACHE=${GOCACHE:-/tmp/mptcp-desktop-cache}
export GOMODCACHE=${GOMODCACHE:-/tmp/mptcp-desktop-mod}
VERSION=$(cat "$ROOT/macos/VERSION")
[[ $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
OUT="$ROOT/dist/userspace-$VERSION"
mkdir -p "$OUT"
SOURCE_ID=$(python3 "$ROOT/scripts/source-manifest.py" --id)
FLAGS="-s -w -buildid= -X mptcp-desktop/engine/multipath.SourceID=$SOURCE_ID"
(
    cd "$ROOT/macos/engine"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -buildvcs=false \
        -ldflags="$FLAGS" -o "$OUT/mptcp-landing" ./cmd/mptcp-landing
)
[[ $(python3 "$ROOT/scripts/source-manifest.py" --id) == "$SOURCE_ID" ]]
chmod 755 "$OUT/mptcp-landing"
cd "$OUT"
shasum -a 256 mptcp-landing > mptcp-landing.sha256
{
    printf 'Component: mptcp-landing\nVersion: %s\nSource-ID: %s\nTarget: linux/amd64\nCGO_ENABLED: 0\nProtocol: MPX/3 experimental\n' "$VERSION" "$SOURCE_ID"
    "$GO" version
    "$GO" version -m mptcp-landing
} > mptcp-landing.BUILDINFO
file mptcp-landing
cat mptcp-landing.sha256
