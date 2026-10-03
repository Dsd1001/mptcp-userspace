#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
GO=${MPTCP_GO:-go}
if [[ $GO == go && -f "$ROOT/macos/build/go-path" ]]; then GO=$(cat "$ROOT/macos/build/go-path"); fi
export GOTOOLCHAIN=local GOCACHE=${GOCACHE:-/tmp/mptcp-provision-cache} GOMODCACHE=${GOMODCACHE:-/tmp/mptcp-provision-mod}
VERSION=$(cat "$ROOT/provisioning/VERSION")
OUT="$ROOT/dist/provisioning-$VERSION"; mkdir -p "$OUT"
SOURCE_ID=$(python3 "$ROOT/scripts/provisioning-source-manifest.py" --id)
build_one(){ arch=$1; out="$OUT/mpx-provision-linux-$arch"; (cd "$ROOT/provisioning"; CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$GO" build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.Version=$VERSION -X main.SourceID=$SOURCE_ID" -o "$out" .); chmod 755 "$out"; file "$out"; shasum -a 256 "$out" > "$out.sha256"; { printf 'Component: mpx-provision\nVersion: %s\nSource-ID: %s\nTarget: linux/%s\nAPI: Provisioning schema 1\n' "$VERSION" "$SOURCE_ID" "$arch"; "$GO" version; "$GO" version -m "$out"; } > "$out.BUILDINFO"; }
build_one amd64; build_one arm64
[[ $(python3 "$ROOT/scripts/provisioning-source-manifest.py" --id) == "$SOURCE_ID" ]]
