#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
VERSION=$(cat "$ROOT/macos/VERSION")
INSTALLER=${1:-"$ROOT/windows/build/bin/MPTCP-Desk-$VERSION-Windows-Setup.exe"}
OUT=${2:-"$(dirname "$INSTALLER")/windows-update.json"}
[[ -f "$INSTALLER" ]] || { echo "Build or download the Windows installer first: $INSTALLER" >&2; exit 1; }

SPARKLE_VERSION=2.10.0
SPARKLE_SHA256=c2bf58aa8387266ac179357b1415d6f2635f044da8be41042af32425dae6da0c
ARCHIVE="$ROOT/macos/build/vendor/Sparkle-$SPARKLE_VERSION.tar.xz"
if [[ ! -f "$ARCHIVE" ]]; then
    mkdir -p "$(dirname "$ARCHIVE")"
    curl -fsSL --retry 3 "https://github.com/sparkle-project/Sparkle/releases/download/$SPARKLE_VERSION/Sparkle-$SPARKLE_VERSION.tar.xz" -o "$ARCHIVE.tmp"
    mv "$ARCHIVE.tmp" "$ARCHIVE"
fi
[[ $(shasum -a 256 "$ARCHIVE" | awk '{print $1}') == "$SPARKLE_SHA256" ]] || { echo "Sparkle archive checksum mismatch" >&2; exit 1; }

TMP=$(mktemp -d /tmp/mptcp-windows-update.XXXXXX)
trap 'rm -rf "$TMP"' EXIT
tar -xJf "$ARCHIVE" -C "$TMP" ./bin/sign_update
SIGNATURE_LINE=$("$TMP/bin/sign_update" "$INSTALLER")
ED_SIGNATURE=$(printf '%s\n' "$SIGNATURE_LINE" | sed -n 's/.*sparkle:edSignature="\([^"]*\)".*/\1/p')
LENGTH=$(stat -f %z "$INSTALLER")
[[ -n "$ED_SIGNATURE" && "$SIGNATURE_LINE" == *"length=\"$LENGTH\""* ]] || { echo "Sparkle signature output invalid" >&2; exit 1; }
SHA256=$(shasum -a 256 "$INSTALLER" | awk '{print $1}')
DOWNLOAD_URL="https://github.com/Dsd1001/mptcp-userspace/releases/download/v$VERSION/MPTCP-Desk-$VERSION-Windows-Setup.exe"
mkdir -p "$(dirname "$OUT")"
python3 - "$VERSION" "$DOWNLOAD_URL" "$SHA256" "$LENGTH" "$ED_SIGNATURE" "$OUT" <<'PY'
import json, pathlib, sys
version, url, sha256, size, signature, out = sys.argv[1:]
payload = {
    "version": version,
    "url": url,
    "sha256": sha256,
    "size": int(size),
    "ed25519_signature": signature,
}
path = pathlib.Path(out)
path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
print(f"windows_update={path}")
print(f"version={version} length={size} sha256={sha256}")
PY
