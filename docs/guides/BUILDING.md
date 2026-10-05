# Building from source

## Toolchain

The release build uses:

- Go for the engine, Linux Client, Landing and Provisioning;
- Swift / Xcode Command Line Tools for the macOS UI;
- Sparkle 2.10.0, pinned by SHA-256;
- standard macOS signing and disk-image tools.

Set `MPTCP_GO` when the required Go toolchain is not the default `go`.

## Build Linux Client

~~~sh
./scripts/build-linux-client.sh
~~~

The script emits amd64 and arm64 binaries plus BUILDINFO and SHA256 files.

## Build Linux Landing

~~~sh
./scripts/build-userspace-landing.sh
~~~

The Landing build uses CGO_ENABLED=0, GOOS=linux and GOARCH=amd64/arm64.

## Build Provisioning

~~~sh
./scripts/build-provisioning.sh
~~~

## Build the macOS DMG

Official updater builds use the pinned long-lived local signing identity:

~~~sh
./macos/build.sh
~~~

The default identity is `MPTCP Desk Stable Local Code Signing`. Its public certificate is pinned at
`macos/signing/MPTCP-Desk-Stable-Local-Code-Signing.crt`; the private key stays only in the release
machine's login Keychain and must never be committed. The build verifies that the Keychain certificate
fingerprint matches the repository pin and records the resulting Designated Requirement in
`MPTCP-Desk.BUILDINFO`.

For a contributor-only ad-hoc build:

~~~sh
MPTCP_CODESIGN_IDENTITY=- ./macos/build.sh
~~~

For a Developer ID build, explicitly select that mode:

~~~sh
MPTCP_CODESIGN_STYLE=developer-id \
MPTCP_CODESIGN_IDENTITY='Developer ID Application: ...' \
./macos/build.sh
~~~

The Developer ID mode enables hardened runtime and timestamping. Notarization remains available through
`MPTCP_NOTARY_PROFILE`, and is only accepted with `MPTCP_CODESIGN_STYLE=developer-id`.

Expected output includes:

- MPTCP-Desk-<version>-universal.dmg
- MPTCP-Desk-<version>-SHA256SUMS
- MPTCP-Desk.BUILDINFO

### Why the stable local certificate exists

MPTCP Desk stores three independent secrets in Keychain: the transport key, Provisioning URL and
remote-management credential. An ad-hoc signature has a content-hash-based Designated Requirement, so a
rebuilt App is a different Keychain client and macOS may ask for authorization again for each item.

The long-lived self-signed code-signing certificate gives successive releases a stable certificate anchor,
bundle identifier and Designated Requirement. This follows Apple's code-signing guidance for Keychain ACL
tracking; Developer ID is not required for this specific identity-continuity property.

Migration from an already-installed ad-hoc build is intentionally one-time: the first stable-signed build may
still require authorization for existing Keychain items because those items were created for the old ad-hoc
requirement. After that authorization, validate with two consecutively built versions signed by the same
certificate. The second update is the important regression test: it should not ask again for the three
Keychain items.

Do not weaken Keychain ACLs, grant all applications access, or move secrets to plaintext storage to avoid
prompts.

The stable certificate is a local identity, not Apple trust/notarization. Gatekeeper distribution and
installation-directory write authorization are separate concerns. Sparkle EdDSA still authenticates the update
payload independently.

### Release key custody

The private key is the continuity identity for all future local-signed releases. Export it once as a
password-protected PKCS#12 backup, store that backup offline, and do not rotate the certificate during ordinary
updates. Losing or replacing this key creates another Keychain identity migration.

## Source identity behavior

The build scripts compute Source-ID before producing binaries and verify that reviewed source does not change
during the build. The pinned public local-signing certificate is part of the source manifest; the private key is
not.

The Git tag for each published version is the authoritative source snapshot for that release. Documentation-only
files under docs/guides and the top-level README files are intentionally outside the release source manifest.

## Validation

Building successfully is not equivalent to release acceptance. A release candidate must also pass the gates in
[Validation](../userspace/VALIDATION.md) and publish source-matched evidence.
### Frozen Keychain Broker

Starting with 0.10.11, the stable self-signed Designated Requirement still authenticates the main app, but the main app no longer accesses the three file-based Keychain items directly. Modern macOS also maintains a separate `partition_id` containing the accessing process cdhash; rebuilding the app changes that cdhash even when its Designated Requirement is stable.

The release therefore carries `macos/keychain-broker/MPTCPKeychainBroker.v1.b64`, the Base64 representation of a signed Universal broker whose bytes are frozen. The build copies this resource verbatim and must never rebuild or re-sign it while calling it v1. MPTCP Desk verifies SHA-256 `5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`, installs those exact bytes once, and reuses them across updates.

Any future broker change requires a new broker protocol/version and an explicit one-time Keychain identity migration; v1 must not be overwritten.
