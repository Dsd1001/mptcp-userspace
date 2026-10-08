# Building MPTCP Userspace v1.1.1

This guide covers source builds for the current v1.1.1 suite. Building artifacts does **not** modify production network settings, services, firewalls or congestion control.

## Version and protocol identity

The current suite uses:

```text
Product version:   1.1.1
Wire protocol:     MPX/4 Protocol Version 4 Stable
Protocol release:  protocol-v4.0.0
Protocol source:   44f587fd279ed2238b070dd68114c76822353f4d
Capability rev:    8
```

`macos/VERSION` and `provisioning/VERSION` must agree for suite builds. Release binaries embed a Source-ID derived from the frozen source manifest.

## Toolchains

The repository uses:

- Go for the userspace engine, Linux Client, Landing and Provisioning;
- Swift + Xcode Command Line Tools for macOS MPTCP Desk;
- Go 1.25 + Wails v2 + WebView2 + NSIS for Windows MPTCP Desk;
- Python 3 for source manifests, packaging and verification;
- standard macOS tools (`codesign`, `hdiutil`, `lipo`, `plutil`) for the Mac artifact.

The scripts prefer `MPTCP_GO` when set, otherwise the pinned path in `macos/build/go-path` when present, otherwise `go` from PATH.

## Linux Client

```sh
./scripts/build-linux-client.sh
```

The script produces static `CGO_ENABLED=0` builds for amd64 and arm64 under `dist/userspace-1.1.1/`:

```text
mptcp-client-linux-amd64
mptcp-client-linux-arm64
```

Each binary gets a SHA256 file and BUILDINFO with version, Source-ID and protocol identity.

## Landing

```sh
./scripts/build-userspace-landing.sh
```

Outputs:

```text
mptcp-landing
mptcp-landing-linux-arm64
```

This script is build-only. It does not install a service and does not change kernel, proxy, firewall or TCP congestion-control settings.

## Provisioning

Suite build:

```sh
./scripts/build-provisioning.sh
```

Outputs include:

```text
mpx-provision
mpx-provision-linux-arm64
```

The default suite scope requires Provisioning and MPTCP Desk/engine versions to match.

## Windows MPTCP Desk

Windows uses the same MPX/4 Userspace engine but intentionally has no Native/kernel MPTCP fallback. Build it on Windows with:

```powershell
.\windows\build.ps1
```

The script produces a per-user NSIS installer and a portable ZIP under `windows/build/bin/`. See `windows/README.zh-CN.md` for the Windows architecture and proxy-chaining model.

## MPTCP Desk

```sh
./macos/build.sh
```

The default build expects the long-lived local signing identity `MPTCP Desk Stable Local Code Signing`. The build also verifies that the frozen `MPTCPKeychainBroker` v1 bytes are unchanged before constructing the app.

The broker SHA256 is pinned to:

```text
5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9
```

The default output is a Universal arm64+x86_64 DMG under `dist/userspace-1.1.1/`.

Developer-ID/notarized builds are supported only when the required signing identity/profile is explicitly provided. Do not confuse local code-signing continuity with Apple notarization.

## Full packaging

The repository also contains:

```text
scripts/package-userspace.py
scripts/build-appcast.sh
scripts/verify-userspace.py
scripts/release-gates.py
scripts/scheduler-gates.py
```

These manage source freeze, release packaging, Sparkle metadata and evidence verification. Release artifacts should be produced from one frozen source identity, not by mixing binaries from different working trees.

## Basic source checks

For the engine:

```sh
cd macos/engine
go test ./... -count=1
go vet ./...
go test -race ./multipath -count=1
```

For Provisioning:

```sh
cd provisioning
go test ./... -count=1
go vet ./...
```

The full release process has additional package/UI/provenance gates; see [Validation](../userspace/VALIDATION.md).

## Network tuning is deployment-time, not build-time

The recommended production host baseline is:

- **Landing: CUBIC**;
- **Relay: BBR**, preferably with `fq`.

Build scripts intentionally do not apply these sysctls. Apply and verify them only during deployment using [Network tuning](NETWORK-TUNING.md).
