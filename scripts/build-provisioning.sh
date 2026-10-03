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

build_one() {
    local arch=$1
    local name
    if [[ $arch == amd64 ]]; then name=mpx-provision; else name=mpx-provision-linux-arm64; fi
    local out="$OUT/$name"
    (
        cd "$ROOT/provisioning"
        CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$GO" build -trimpath -buildvcs=false \
            -ldflags='-s -w -buildid=' -o "$out" .
    )
    chmod 755 "$out"
    case "$arch" in
        amd64) file "$out" | grep -E 'ELF 64-bit.*x86-64.*statically linked' ;;
        arm64) file "$out" | grep -E 'ELF 64-bit.*ARM aarch64.*statically linked' ;;
    esac
    {
        printf 'Component: mpx-provision\nVersion: %s\nSource-ID: %s\nTarget: linux/%s\nCGO_ENABLED: 0\nAPI: Provisioning schema 1\n' "$VERSION" "$SOURCE_ID" "$arch"
        "$GO" version
        "$GO" version -m "$out"
    } > "$out.BUILDINFO"
    shasum -a 256 "$out" > "$out.sha256"
}

build_one amd64
build_one arm64
[[ $(python3 "$ROOT/scripts/source-manifest.py" --id) == "$SOURCE_ID" ]]
cat "$OUT/mpx-provision.sha256"
cat "$OUT/mpx-provision-linux-arm64.sha256"
