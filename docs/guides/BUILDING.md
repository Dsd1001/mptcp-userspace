# Building from source

[中文版](BUILDING.zh-CN.md)

For reproducible v0.9.5 work, check out the release tag first:

~~~sh
git checkout v0.9.5
~~~

The release source identity is:

~~~text
3e2b06db8bc7d5ef3580c825e3cb16ac7f76b99c093706ce52ee17c51f05225f
~~~

Verify it with:

~~~sh
python3 scripts/source-manifest.py --id
~~~

## Toolchain

The Go module declares Go 1.23.0.

The full macOS DMG build also uses Apple command-line tools including:

- xcrun / macOS SDK
- swiftc
- lipo
- iconutil
- codesign
- hdiutil

The build targets macOS 13+ and produces arm64/x86_64 Universal application binaries.

## Build Linux Landing

~~~sh
./scripts/build-userspace-landing.sh
~~~

You may select a Go binary explicitly:

~~~sh
MPTCP_GO=/path/to/go ./scripts/build-userspace-landing.sh
~~~

Output is written under the versioned dist/userspace-<version>/ directory and includes:

- mptcp-landing
- mptcp-landing.sha256
- mptcp-landing.BUILDINFO

The Landing build uses CGO_ENABLED=0, GOOS=linux and GOARCH=amd64.

## Build the macOS DMG

On macOS with the required Apple toolchain:

~~~sh
./macos/build.sh
~~~

The script builds both arm64 and x86_64 Swift/Go components, combines them into a Universal app, signs the app, verifies the signature, and creates the DMG. By default it keeps the local ad-hoc signing behavior. For a distributable updater build, set `MPTCP_CODESIGN_IDENTITY` to a Developer ID Application identity; the script then signs the app and embedded Sparkle helpers with runtime hardening and a secure timestamp.

Expected output includes:

- MPTCP-Desk-<version>-universal.dmg
- MPTCP-Desk-<version>-SHA256SUMS
- MPTCP-Desk.BUILDINFO

The default app is ad-hoc signed and the DMG is not notarized. To notarize a release, store App Store Connect credentials in a notarytool keychain profile and pass `MPTCP_NOTARY_PROFILE`; this requires a non-ad-hoc `MPTCP_CODESIGN_IDENTITY`. Signing and notarization do not guarantee an unattended update:

- **Keychain access:** the app stores the provisioning URL, transport key, and remote-management credential as three separate Keychain items. The current ad-hoc signature has a designated requirement based on code hashes, which change when the app is rebuilt. Existing Keychain permissions may therefore prompt again after an update, potentially for multiple items. Release builds should preserve the bundle identifier and a compatible Developer ID signing requirement across versions. Migration from existing ad-hoc builds may still require local authorization for the existing items; a locked Keychain can also require interaction. The app must not grant all applications access or fall back to plaintext storage to suppress these prompts.
- **Installation authorization:** replacing an app depends on ownership and write permissions for the installed app and its location. A Developer ID signature does not grant administrator privileges or remove a required installation authorization prompt.
- **Notarization and update signatures:** Apple notarization addresses Gatekeeper distribution checks. Sparkle's EdDSA signature verifies update authenticity. Neither grants access to Keychain items or protected installation locations.

The Developer ID/notarization build path requires a usable signing identity on the build machine. Its presence in the script alone does not mean a release was built with it or that unattended updates have been verified.

## Source identity behavior

The build scripts compute Source-ID before producing binaries and verify that the reviewed source did not change during the build.

The v0.9.5 Git tag is the authoritative source snapshot for the published release assets. Documentation-only files under docs/guides and the top-level README files are intentionally outside the release source manifest, so improving public usage documentation does not change the frozen release Source-ID.

## Validation

Building successfully is not equivalent to release acceptance.

The repository contains release-gate and scheduler-gate scripts, but the published v0.9.5 evidence in the GitHub Release is the authoritative record of which gates passed and which higher-level runtime tests were not rerun.

See:

- [Validation](../userspace/VALIDATION.md)
- [v0.9.5 release notes](../userspace/RELEASE.zh-CN.md)
