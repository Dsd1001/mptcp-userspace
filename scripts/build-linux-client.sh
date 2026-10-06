#!/usr/bin/env bash
# Build the headless Linux Userspace MPX/4 client for amd64 and arm64.
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

build_one() {
    local arch=$1
    local out="$OUT/mptcp-client-linux-$arch"
    (
        cd "$ROOT/macos/engine"
        CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$GO" build -trimpath -buildvcs=false \
            -ldflags="$FLAGS" -o "$out" .
    )
    chmod 755 "$out"
    case "$arch" in
        amd64) file "$out" | grep -E 'ELF 64-bit.*x86-64.*statically linked' ;;
        arm64) file "$out" | grep -E 'ELF 64-bit.*ARM aarch64.*statically linked' ;;
    esac
    shasum -a 256 "$out" > "$out.sha256"
    {
        printf 'Component: mptcp-client\nVersion: %s\nSource-ID: %s\nTarget: linux/%s\nCGO_ENABLED: 0\nProtocol: MPX/4 Protocol Version 4 Stable\nProtocol-Release: protocol-v4.0.0\nProtocol-Source: 44f587fd279ed2238b070dd68114c76822353f4d\nMode: userspace_multipath only\n' "$VERSION" "$SOURCE_ID" "$arch"
        "$GO" version
        "$GO" version -m "$out"
    } > "$out.BUILDINFO"
}

build_one amd64
build_one arm64
[[ $(python3 "$ROOT/scripts/source-manifest.py" --id) == "$SOURCE_ID" ]]
cat "$OUT/mptcp-client-linux-amd64.sha256"
cat "$OUT/mptcp-client-linux-arm64.sha256"
