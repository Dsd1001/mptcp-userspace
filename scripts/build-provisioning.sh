#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
GO=${MPTCP_GO:-go}
if [[ $GO == go && -f "$ROOT/macos/build/go-path" ]]; then GO=$(cat "$ROOT/macos/build/go-path"); fi
export GOCACHE=${GOCACHE:-/tmp/mptcp-provision-cache}
export GOMODCACHE=${GOMODCACHE:-/tmp/mptcp-provision-mod}
export GOTOOLCHAIN=local
VERSION=$(cat "$ROOT/macos/VERSION")
OUT="$ROOT/dist/userspace-$VERSION"
mkdir -p "$OUT"
SOURCE_ID=$(python3 "$ROOT/scripts/source-manifest.py" --id)
(
    cd "$ROOT/provisioning"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -buildvcs=false \
        -ldflags='-s -w -buildid=' -o "$OUT/mpx-provision" .
)
file "$OUT/mpx-provision" | grep -E 'ELF 64-bit.*x86-64.*statically linked'
{
    printf 'Component: mpx-provision\nVersion: %s\nSource-ID: %s\nTarget: linux/amd64\nCGO_ENABLED: 0\nAPI: Provisioning schema 1\n' "$VERSION" "$SOURCE_ID"
    "$GO" version
} > "$OUT/mpx-provision.BUILDINFO"
cd "$OUT"
shasum -a 256 mpx-provision > mpx-provision.sha256
cat mpx-provision.sha256
