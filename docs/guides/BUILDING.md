# Building from source

[中文版](BUILDING.zh-CN.md)

For reproducible v0.9.4 work, check out the release tag first:

~~~sh
git checkout v0.9.4
~~~

The release source identity is:

~~~text
d4b8f362a8179257f2889abfea587c480755470bac6a78d8c023c3496c66f2bf
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

The script builds both arm64 and x86_64 Swift/Go components, combines them into a Universal app, ad-hoc signs the app, verifies the signature, and creates the DMG.

Expected output includes:

- MPTCP-Desk-<version>-universal.dmg
- MPTCP-Desk-<version>-SHA256SUMS
- MPTCP-Desk.BUILDINFO

The DMG is ad-hoc signed and not notarized.

## Source identity behavior

The build scripts compute Source-ID before producing binaries and verify that the reviewed source did not change during the build.

The v0.9.4 Git tag is the authoritative source snapshot for the published release assets. Documentation-only files under docs/guides and the top-level README files are intentionally outside the release source manifest, so improving public usage documentation does not change the frozen release Source-ID.

## Validation

Building successfully is not equivalent to release acceptance.

The repository contains release-gate and scheduler-gate scripts, but the published v0.9.4 evidence in the GitHub Release is the authoritative record of which gates passed and which higher-level runtime tests were not rerun.

See:

- [Validation](../userspace/VALIDATION.md)
- [v0.9.4 release notes](../userspace/RELEASE.zh-CN.md)
