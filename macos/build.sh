#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BUILD=$ROOT/macos/build
GO=${MPTCP_GO:-go}
if [[ $GO == go && -f "$BUILD/go-path" ]]; then GO=$(cat "$BUILD/go-path"); fi
export GOCACHE=${GOCACHE:-/tmp/mptcp-desktop-cache}
export GOMODCACHE=${GOMODCACHE:-/tmp/mptcp-desktop-mod}
export GOTOOLCHAIN=local
VERSION=$(cat "$ROOT/macos/VERSION")
[[ $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
OUT="$ROOT/dist/userspace-$VERSION"
mkdir -p "$BUILD" "$OUT"
SOURCE_ID=$(python3 "$ROOT/scripts/source-manifest.py" --id)
SDK=$(xcrun --sdk macosx --show-sdk-path)
FLAGS="-s -w -buildid= -X mptcp-desktop/engine/multipath.SourceID=$SOURCE_ID"
for arch in arm64 amd64; do
    swift_arch=$arch
    [[ $arch != amd64 ]] || swift_arch=x86_64
    xcrun swiftc -O -swift-version 5 -parse-as-library -target "$swift_arch-apple-macosx13.0" \
        -module-cache-path /tmp/mptcp-swift-cache -debug-prefix-map "$ROOT"=. \
        "$ROOT/macos/Profile.swift" "$ROOT/macos/App.swift" -o "$BUILD/MPTCPDesk-$arch"
    (
        cd "$ROOT/macos/engine"
        CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="clang -arch $swift_arch -isysroot $SDK -mmacosx-version-min=13.0" \
            "$GO" build -trimpath -buildvcs=false -ldflags="$FLAGS" -o "$BUILD/engine-$arch" .
    )
done
APPROOT=$(mktemp -d /tmp/mptcp-app.XXXXXX)
STAGE=$(mktemp -d /tmp/mptcp-dmg.XXXXXX)
trap 'rm -rf "$STAGE" "$APPROOT"' EXIT
APP="$APPROOT/MPTCP Desk.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create "$BUILD/MPTCPDesk-arm64" "$BUILD/MPTCPDesk-amd64" -output "$APP/Contents/MacOS/MPTCPDesk"
lipo -create "$BUILD/engine-arm64" "$BUILD/engine-amd64" -output "$APP/Contents/Resources/mptcp-desktop-engine"
cp "$ROOT/macos/Info.plist" "$APP/Contents/Info.plist"
[[ $(plutil -extract CFBundleShortVersionString raw -o - "$APP/Contents/Info.plist") == "$VERSION" ]]
plutil -insert MPTCPSourceID -string "$SOURCE_ID" "$APP/Contents/Info.plist"
printf '%s\n' "$SOURCE_ID" > "$APP/Contents/Resources/SOURCE_ID"
xcrun swiftc "$ROOT/macos/Icon.swift" -module-cache-path /tmp/mptcp-swift-cache -o "$BUILD/icon-generator"
"$BUILD/icon-generator" "$APPROOT/AppIcon.iconset"
iconutil -c icns "$APPROOT/AppIcon.iconset" -o "$APP/Contents/Resources/AppIcon.icns"
for name in README.zh-CN.md VALIDATION.md tcp-profile.example.json userspace-profile.example.json; do
    cp "$ROOT/macos/$name" "$APP/Contents/Resources/$name"
    cp "$ROOT/macos/$name" "$STAGE/$name"
done
mkdir -p "$APP/Contents/Resources/docs/userspace" "$STAGE/docs/userspace"
for name in DEPLOYMENT.zh-CN.md PROTOCOL.md VALIDATION.md ADAPTIVE-FLOW-CONTROL.md MPX3-CREDIT.md SCHEDULER-MODES.md REV2-SHARED-CREDIT.md; do
    cp "$ROOT/docs/userspace/$name" "$APP/Contents/Resources/docs/userspace/"
    cp "$ROOT/docs/userspace/$name" "$STAGE/docs/userspace/"
done
mkdir -p "$APP/Contents/Resources/Licenses"
cp -f "$("$GO" env GOROOT)/LICENSE" "$APP/Contents/Resources/Licenses/go.txt"
codesign --force --sign - "$APP/Contents/Resources/mptcp-desktop-engine"
codesign --force --sign - "$APP"
codesign --verify --deep --strict "$APP"
for binary in "$APP/Contents/MacOS/MPTCPDesk" "$APP/Contents/Resources/mptcp-desktop-engine"; do
    archs=$(lipo -archs "$binary")
    printf 'Universal architectures: %s\n' "$archs"
    [[ " $archs " == *" arm64 "* && " $archs " == *" x86_64 "* ]]
done
[[ $(plutil -extract LSUIElement raw -o - "$APP/Contents/Info.plist") == true ]]
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
printf '%s\n' "$SOURCE_ID" > "$STAGE/SOURCE_ID"
hdiutil create -ov -volname 'MPTCP Desk' -srcfolder "$STAGE" -format UDZO "$OUT/MPTCP-Desk-$VERSION-universal.dmg"
hdiutil verify "$OUT/MPTCP-Desk-$VERSION-universal.dmg"
[[ $(python3 "$ROOT/scripts/source-manifest.py" --id) == "$SOURCE_ID" ]]
{
    printf 'Component: MPTCP Desk\nVersion: %s\nSource-ID: %s\nProtocol: MPX/3 capability revision 5\nArchitectures: arm64 x86_64\nSigning: ad-hoc, not notarized\n' "$VERSION" "$SOURCE_ID"
    "$GO" version
    xcrun swiftc --version
} > "$OUT/MPTCP-Desk.BUILDINFO"
cd "$OUT"
shasum -a 256 "MPTCP-Desk-$VERSION-universal.dmg" > "MPTCP-Desk-$VERSION-SHA256SUMS"
