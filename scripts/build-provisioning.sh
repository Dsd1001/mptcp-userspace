#!/usr/bin/env bash
# Build Provisioning as part of the full MPTCP Userspace release.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
GO=${MPTCP_GO:-go}
if [[ $GO == go && -f "$ROOT/macos/build/go-path" ]]; then GO=$(cat "$ROOT/macos/build/go-path"); fi
export GOTOOLCHAIN=local GOCACHE=${GOCACHE:-/tmp/mptcp-provision-cache} GOMODCACHE=${GOMODCACHE:-/tmp/mptcp-provision-mod}
VERSION=$(cat "$ROOT/provisioning/VERSION")
[[ $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
SCOPE=${MPX_PROVISION_BUILD_SCOPE:-suite}
if [[ $SCOPE == suite ]]; then
    SUITE_VERSION=$(cat "$ROOT/macos/VERSION")
    [[ $VERSION == "$SUITE_VERSION" ]]
    OUT="$ROOT/dist/userspace-$VERSION"
    SOURCE_ID=$(python3 "$ROOT/scripts/source-manifest.py" --id)
else
    [[ $SCOPE == standalone ]]
    OUT="$ROOT/dist/provisioning-$VERSION"
    SOURCE_ID=$(python3 "$ROOT/scripts/provisioning-source-manifest.py" --id)
fi
mkdir -p "$OUT"
FLAGS="-s -w -buildid= -X main.Version=$VERSION -X main.SourceID=$SOURCE_ID"

build_one() {
    local arch=$1
    local name
    if [[ $SCOPE == suite ]]; then
        if [[ $arch == amd64 ]]; then name=mpx-provision; else name=mpx-provision-linux-arm64; fi
    else
        name=mpx-provision-linux-$arch
    fi
    local out="$OUT/$name"
    (
        cd "$ROOT/provisioning"
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
        printf 'Component: mpx-provision\nVersion: %s\nSource-ID: %s\nTarget: linux/%s\nCGO_ENABLED: 0\nAPI: Profile schema 1 + Bundle schema 2\n' "$VERSION" "$SOURCE_ID" "$arch"
        "$GO" version
        "$GO" version -m "$out"
    } > "$out.BUILDINFO"
}

build_one amd64
build_one arm64
if [[ $SCOPE == suite ]]; then
    [[ $(python3 "$ROOT/scripts/source-manifest.py" --id) == "$SOURCE_ID" ]]
    cat "$OUT/mpx-provision.sha256"
    cat "$OUT/mpx-provision-linux-arm64.sha256"
else
    [[ $(python3 "$ROOT/scripts/provisioning-source-manifest.py" --id) == "$SOURCE_ID" ]]
    cat "$OUT/mpx-provision-linux-amd64.sha256"
    cat "$OUT/mpx-provision-linux-arm64.sha256"
fi
