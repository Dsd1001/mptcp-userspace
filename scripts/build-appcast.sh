#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
VERSION=$(cat "$ROOT/macos/VERSION")
OUT="$ROOT/dist/userspace-$VERSION"
DMG="$OUT/MPTCP-Desk-$VERSION-universal.dmg"
[[ -f "$DMG" ]] || { echo "Build the macOS DMG before generating appcast" >&2; exit 1; }
BUILD_VERSION=$(plutil -extract CFBundleVersion raw -o - "$ROOT/macos/Info.plist")
[[ "$BUILD_VERSION" =~ ^[0-9]+$ ]] || { echo "Invalid CFBundleVersion" >&2; exit 1; }

SPARKLE_VERSION=2.10.0
SPARKLE_SHA256=c2bf58aa8387266ac179357b1415d6f2635f044da8be41042af32425dae6da0c
ARCHIVE="$ROOT/macos/build/vendor/Sparkle-$SPARKLE_VERSION.tar.xz"
if [[ ! -f "$ARCHIVE" ]]; then
    mkdir -p "$(dirname "$ARCHIVE")"
    curl -fsSL --retry 3 "https://github.com/sparkle-project/Sparkle/releases/download/$SPARKLE_VERSION/Sparkle-$SPARKLE_VERSION.tar.xz" -o "$ARCHIVE.tmp"
    mv "$ARCHIVE.tmp" "$ARCHIVE"
fi
[[ $(shasum -a 256 "$ARCHIVE" | awk '{print $1}') == "$SPARKLE_SHA256" ]] || { echo "Sparkle archive checksum mismatch" >&2; exit 1; }

TMP=$(mktemp -d /tmp/mptcp-appcast.XXXXXX)
trap 'rm -rf "$TMP"' EXIT
tar -xJf "$ARCHIVE" -C "$TMP" ./bin/sign_update
SIGNATURE_LINE=$("$TMP/bin/sign_update" "$DMG")
ED_SIGNATURE=$(printf '%s\n' "$SIGNATURE_LINE" | sed -n 's/.*sparkle:edSignature="\([^"]*\)".*/\1/p')
LENGTH=$(stat -f %z "$DMG")
[[ -n "$ED_SIGNATURE" && "$SIGNATURE_LINE" == *"length=\"$LENGTH\""* ]] || { echo "Sparkle signature output invalid" >&2; exit 1; }

DOWNLOAD_URL="https://github.com/Dsd1001/mptcp-userspace/releases/download/v$VERSION/MPTCP-Desk-$VERSION-universal.dmg"
RELEASE_URL="https://github.com/Dsd1001/mptcp-userspace/releases/tag/v$VERSION"
cat > "$OUT/appcast.xml" <<XML
<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle">
  <channel>
    <title>MPTCP Desk Updates</title>
    <link>https://github.com/Dsd1001/mptcp-userspace</link>
    <description>Signed MPTCP Desk update feed</description>
    <language>zh-cn</language>
    <item>
      <title>MPTCP Desk $VERSION</title>
      <sparkle:version>$BUILD_VERSION</sparkle:version>
      <sparkle:shortVersionString>$VERSION</sparkle:shortVersionString>
      <sparkle:minimumSystemVersion>13.0</sparkle:minimumSystemVersion>
      <sparkle:releaseNotesLink>$RELEASE_URL</sparkle:releaseNotesLink>
      <enclosure url="$DOWNLOAD_URL" length="$LENGTH" type="application/octet-stream" sparkle:edSignature="$ED_SIGNATURE" />
    </item>
  </channel>
</rss>
XML
"$TMP/bin/sign_update" --disable-signing-warning "$OUT/appcast.xml"
"$TMP/bin/sign_update" --verify "$OUT/appcast.xml"
xmllint --noout "$OUT/appcast.xml"
printf 'appcast=%s\n' "$OUT/appcast.xml"
printf 'version=%s build=%s length=%s\n' "$VERSION" "$BUILD_VERSION" "$LENGTH"
