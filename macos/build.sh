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
SPARKLE_VERSION=2.10.0
SPARKLE_SHA256=c2bf58aa8387266ac179357b1415d6f2635f044da8be41042af32425dae6da0c
SPARKLE_VENDOR="$BUILD/vendor/sparkle-$SPARKLE_VERSION"
SPARKLE_ARCHIVE="$BUILD/vendor/Sparkle-$SPARKLE_VERSION.tar.xz"
STABLE_LOCAL_IDENTITY='MPTCP Desk Stable Local Code Signing'
STABLE_LOCAL_CERT="$ROOT/macos/signing/MPTCP-Desk-Stable-Local-Code-Signing.crt"
CODESIGN_IDENTITY=${MPTCP_CODESIGN_IDENTITY:-$STABLE_LOCAL_IDENTITY}
CODESIGN_STYLE=${MPTCP_CODESIGN_STYLE:-local}
NOTARY_PROFILE=${MPTCP_NOTARY_PROFILE:-}

if [[ "$CODESIGN_IDENTITY" == "-" ]]; then CODESIGN_STYLE=adhoc; fi
case "$CODESIGN_STYLE" in
    adhoc|local|developer-id) ;;
    *) echo "Unsupported MPTCP_CODESIGN_STYLE: $CODESIGN_STYLE" >&2; exit 1 ;;
esac
if [[ "$CODESIGN_STYLE" == local ]]; then
    [[ "$CODESIGN_IDENTITY" == "$STABLE_LOCAL_IDENTITY" ]] || { echo "local signing must use $STABLE_LOCAL_IDENTITY" >&2; exit 1; }
    [[ -f "$STABLE_LOCAL_CERT" ]] || { echo "Pinned local signing certificate missing: $STABLE_LOCAL_CERT" >&2; exit 1; }
    pinned_sha256=$(openssl x509 -in "$STABLE_LOCAL_CERT" -noout -fingerprint -sha256 | cut -d= -f2 | tr -d :)
    keychain_pem=$(security find-certificate -p -c "$STABLE_LOCAL_IDENTITY" "$HOME/Library/Keychains/login.keychain-db" 2>/dev/null || true)
    [[ -n "$keychain_pem" ]] || { echo "Stable local signing identity is not installed in the login Keychain" >&2; exit 1; }
    keychain_sha256=$(printf '%s\n' "$keychain_pem" | openssl x509 -noout -fingerprint -sha256 | cut -d= -f2 | tr -d :)
    [[ "$keychain_sha256" == "$pinned_sha256" ]] || { echo "Stable local signing certificate fingerprint differs from repository pin" >&2; exit 1; }
    security find-identity -v -p codesigning "$HOME/Library/Keychains/login.keychain-db" | grep -F "$STABLE_LOCAL_IDENTITY" >/dev/null || {
        echo "Stable local signing certificate exists but is not trusted/usable for code signing." >&2
        echo "Open Keychain Access, trust this certificate for Code Signing, then retry." >&2
        exit 1
    }
fi
if [[ -n "$NOTARY_PROFILE" && "$CODESIGN_STYLE" != developer-id ]]; then
    echo 'MPTCP_NOTARY_PROFILE requires MPTCP_CODESIGN_STYLE=developer-id' >&2
    exit 1
fi

sign_target() {
    local target="$1"
    case "$CODESIGN_STYLE" in
        adhoc) codesign --force --sign - "$target" ;;
        local) codesign --force --sign "$CODESIGN_IDENTITY" "$target" ;;
        developer-id) codesign --force --options runtime --timestamp --sign "$CODESIGN_IDENTITY" "$target" ;;
    esac
}
mkdir -p "$BUILD/vendor"
if [[ ! -f "$SPARKLE_ARCHIVE" ]]; then
    curl -fsSL --retry 3 "https://github.com/sparkle-project/Sparkle/releases/download/$SPARKLE_VERSION/Sparkle-$SPARKLE_VERSION.tar.xz" -o "$SPARKLE_ARCHIVE.tmp"
    mv "$SPARKLE_ARCHIVE.tmp" "$SPARKLE_ARCHIVE"
fi
[[ $(shasum -a 256 "$SPARKLE_ARCHIVE" | awk '{print $1}') == "$SPARKLE_SHA256" ]] || { echo "Sparkle archive checksum mismatch" >&2; exit 1; }
if [[ ! -d "$SPARKLE_VENDOR/Sparkle.framework" ]]; then
    rm -rf "$SPARKLE_VENDOR"
    mkdir -p "$SPARKLE_VENDOR"
    tar -xJf "$SPARKLE_ARCHIVE" -C "$SPARKLE_VENDOR" ./Sparkle.framework ./LICENSE
fi
PLIST_VERSION=$(plutil -extract CFBundleShortVersionString raw -o - "$ROOT/macos/Info.plist")
[[ "$PLIST_VERSION" == "$VERSION" ]] || { printf 'Info.plist version %s does not match VERSION %s\n' "$PLIST_VERSION" "$VERSION" >&2; exit 1; }
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
        -F "$SPARKLE_VENDOR" -framework Sparkle -Xlinker -rpath -Xlinker @executable_path/../Frameworks         "$ROOT/macos/Lifecycle.swift" "$ROOT/macos/Profile.swift" "$ROOT/macos/RemoteControl.swift" "$ROOT/macos/UpdateController.swift" "$ROOT/macos/App.swift" -o "$BUILD/MPTCPDesk-$arch"
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
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$APP/Contents/Frameworks"
lipo -create "$BUILD/MPTCPDesk-arm64" "$BUILD/MPTCPDesk-amd64" -output "$APP/Contents/MacOS/MPTCPDesk"
lipo -create "$BUILD/engine-arm64" "$BUILD/engine-amd64" -output "$APP/Contents/Resources/mptcp-desktop-engine"
ditto "$SPARKLE_VENDOR/Sparkle.framework" "$APP/Contents/Frameworks/Sparkle.framework"
xattr -cr "$APP/Contents/Frameworks/Sparkle.framework"
cp "$SPARKLE_VENDOR/LICENSE" "$APP/Contents/Resources/Licenses-sparkle.tmp"
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
for name in DEPLOYMENT.zh-CN.md PROTOCOL.md PROVISIONING.md VALIDATION.md ADAPTIVE-FLOW-CONTROL.md MPX3-CREDIT.md SCHEDULER-MODES.md REV2-SHARED-CREDIT.md; do
    cp "$ROOT/docs/userspace/$name" "$APP/Contents/Resources/docs/userspace/"
    cp "$ROOT/docs/userspace/$name" "$STAGE/docs/userspace/"
done
mkdir -p "$APP/Contents/Resources/Licenses"
mv "$APP/Contents/Resources/Licenses-sparkle.tmp" "$APP/Contents/Resources/Licenses/sparkle.txt"
cp -f "$("$GO" env GOROOT)/LICENSE" "$APP/Contents/Resources/Licenses/go.txt"
sign_target "$APP/Contents/Resources/mptcp-desktop-engine"
case "$CODESIGN_STYLE" in
    adhoc) codesign --force --deep --sign - "$APP" ;;
    local) codesign --force --deep --sign "$CODESIGN_IDENTITY" "$APP" ;;
    developer-id) codesign --force --deep --options runtime --timestamp --sign "$CODESIGN_IDENTITY" "$APP" ;;
esac
codesign --verify --deep --strict "$APP"
DESIGNATED_REQUIREMENT=$(codesign -d -r- "$APP" 2>&1 | sed -n -E 's/^#?[[:space:]]*designated => //p')
[[ -n "$DESIGNATED_REQUIREMENT" ]] || { echo 'Unable to read App designated requirement' >&2; exit 1; }
if [[ "$CODESIGN_STYLE" == local ]]; then
    cert_sha1=$(openssl x509 -in "$STABLE_LOCAL_CERT" -noout -fingerprint -sha1 | cut -d= -f2 | tr -d : | tr '[:upper:]' '[:lower:]')
    printf '%s' "$DESIGNATED_REQUIREMENT" | tr '[:upper:]' '[:lower:]' | grep -F "$cert_sha1" >/dev/null || {
        echo 'App designated requirement is not anchored to the pinned stable local certificate' >&2
        exit 1
    }
fi
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
if [[ -n "$NOTARY_PROFILE" ]]; then
    xcrun notarytool submit "$OUT/MPTCP-Desk-$VERSION-universal.dmg" --keychain-profile "$NOTARY_PROFILE" --wait
    xcrun stapler staple "$OUT/MPTCP-Desk-$VERSION-universal.dmg"
    xcrun stapler validate "$OUT/MPTCP-Desk-$VERSION-universal.dmg"
fi
[[ $(python3 "$ROOT/scripts/source-manifest.py" --id) == "$SOURCE_ID" ]]
{
    case "$CODESIGN_STYLE" in
        adhoc) signing='ad-hoc, not notarized' ;;
        local) signing="stable-local self-signed ($CODESIGN_IDENTITY), cert-sha256=$pinned_sha256" ;;
        developer-id) signing="Developer ID ($CODESIGN_IDENTITY)" ;;
    esac
    [[ -z "$NOTARY_PROFILE" ]] || signing="$signing, notarized"
    printf 'Component: MPTCP Desk\nVersion: %s\nSource-ID: %s\nProtocol: MPX/4 Draft 04\nArchitectures: arm64 x86_64\nUpdater: Sparkle %s / EdDSA appcast\nSigning: %s\nDesignated-Requirement: %s\n' "$VERSION" "$SOURCE_ID" "$SPARKLE_VERSION" "$signing" "$DESIGNATED_REQUIREMENT"
    "$GO" version
    xcrun swiftc --version
} > "$OUT/MPTCP-Desk.BUILDINFO"
cd "$OUT"
shasum -a 256 "MPTCP-Desk-$VERSION-universal.dmg" > "MPTCP-Desk-$VERSION-SHA256SUMS"
