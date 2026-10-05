# MPTCP Userspace

MPTCP Userspace is an **application-layer multipath transport** for macOS and Linux. It combines multiple ordinary TCP Carrier connections into an authenticated **MPX/4** Session, multiplexes application TCP Streams across those Carriers, and can retransmit/reinject reliable data when a path degrades or disappears.

It is **not kernel MPTCP** and it is **not QUIC**. Relay nodes only forward ordinary TCP bytes; MPX/4 authentication, encryption, stream state, flow control and scheduling are end-to-end between the Client and Landing.

**Current macOS release: v0.10.11 · MPX/4 Draft 04**

- [Latest release](https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.10.11)
- [MPX/4 specification](https://github.com/Dsd1001/MPX-4)
- [中文说明](README.zh-CN.md)

## v0.10.11: frozen Keychain Broker

0.10.11 separates Keychain access from the replaceable MPTCP Desk app. A pinned, byte-for-byte frozen `MPTCPKeychainBroker` v1 is installed once under the user Application Support directory and owns all access to the existing Transport Key, Provisioning URL and Remote Control credential. The main app no longer calls Keychain APIs directly.

The broker accepts requests only from a parent process satisfying the pinned MPTCP Desk signing requirement. Its signed bytes and cdhash stay unchanged across app updates, so the file-based Keychain partition list is stable even when the main app cdhash changes. The first 0.10.10 → 0.10.11 migration can still ask once for each existing Keychain item; subsequent releases keep using the same broker.

## v0.10.10: signing continuity validation

0.10.10 is the second consecutive MPTCP Desk build using the same long-lived local code-signing identity. It exists to validate that Keychain permissions granted with “Always Allow” on 0.10.9 remain valid after a built-in Sparkle update. MPX/4 and runtime behavior are unchanged.

## v0.10.9: stable macOS signing identity

0.10.9 is a macOS-only signing-continuity update for MPTCP Desk. The app keeps the same `org.mptcp.desktop` bundle identifier and is signed with the long-lived `MPTCP Desk Stable Local Code Signing` identity so Keychain can recognize successive updater builds as the same application. MPX/4, scheduler behavior, Linux Client, Landing and Provisioning behavior are unchanged.

The first migration from an older ad-hoc-signed build can still require Keychain authorization for existing secrets. Updates between releases signed by this same identity are the continuity path being validated by 0.10.9.

## v0.10.7: refreshed macOS and Provisioning interfaces

0.10.7 ships the redesigned **MPTCP Desk** interface and refreshed **Provisioning** web console. Navigation, status presentation, configuration editing and device-management views were reorganized without changing the underlying Profile/Bundle or Device Control APIs.

The signed Sparkle 2 update channel, opt-in remote device management, 0.10.5 parallel Profile supervisor and 0.10.4 Last Known Good cache behavior are retained. **MPX/4 stays on Draft 04 / WireProtocol 4 / CapabilityRevision 4**, with no scheduler, flow-control or key-schedule change in this patch release.

## What is included

| Component | Role | Published platforms |
|---|---|---|
| **MPTCP Desk** | macOS GUI client and local transparent TCP entry | macOS arm64 + x86_64 Universal |
| **Headless Client** | CLI/runtime client for server-side or non-GUI use | Linux amd64 + arm64 |
| **Landing** | MPX/4 endpoint that opens backend TCP connections | Linux amd64 + arm64 |
| **Provisioning** | Optional web/API configuration plane for Profiles and Bundles | Linux amd64 + arm64 |
| **Relay** | Ordinary TCP forwarding hop; no MPX/4 awareness required | Any compatible TCP forwarder |

Release artifacts include the Universal DMG, Linux Client, Landing, Provisioning binaries, frozen source archive, checksums, build metadata, provenance and validation records.

## Architecture

~~~text
Application / Surge
        |
        v
127.0.0.1:<listen_port>
        |
        v
MPTCP Desk / Headless Client
        |
        |  authenticated MPX/4 Session
        |
        +-- TCP Carrier 1 --> Relay A --+
        +-- TCP Carrier 2 --> Relay B --+--> Landing --> backend
        '-- TCP Carrier N --> Relay N --+

Optional Provisioning
        |
        +-- Profile URL --> one complete runtime configuration
        '-- Bundle URL  --> multiple Profiles
                           |-- single_select
                           '-- parallel
~~~

A **Profile** owns one local listener, one MPX Session, its Relay/Carrier set, scheduler, transport key and TCP/UDP settings. A **Bundle** groups 1–32 Profiles for client-side selection or parallel execution. Parallel Profiles remain independent Sessions; their Relay lists are never merged.

## Key capabilities

- **MPX/4 Draft 04 over ordinary TCP Carriers** with authenticated CREATE/JOIN and Carrier Generation replacement semantics.
- **Stream multiplexing** with Session/Stream credit, bounded memory accounting and reliable Transmission IDs.
- **Cross-Carrier retransmission and reinjection** without changing logical Stream byte identity.
- Four scheduler policies: **Auto**, **Aggregate**, **Protect** and **Weighted**.
- **Weighted capacity hints** plus live RTT, queue, delivery, penalty and path usability signals.
- **Parallel Provisioning Bundles**: one failed Profile does not terminate healthy Profiles after atomic local-port preflight.
- **Opaque managed configuration responses**: public Profile/Bundle URLs return a compact AES-256-GCM envelope instead of readable Relay/transport-key JSON.
- **Per-Profile diagnostics** for remote Bundles, including scheduler state, reorder/pending/retransmit counters, resource accounting and path RTT/goodput/queue/outstanding/error telemetry.
- Customer-facing remote diagnostics hide Relay IP/port and raw endpoint errors.
- Optional macOS **background-resident** recovery after login, wake, network restoration or engine restart.
- Independent authenticated **MPU/1 UDP** data plane when UDP is enabled. UDP is not an MPX/4 Core Datagram extension.

## Provisioning model

Provisioning is optional and does not sit in the data path.

A Profile contains the authoritative runtime configuration:

- transport mode;
- local listen_port;
- TCP/UDP enablement;
- scheduler policy;
- currently **2–8 Relay endpoints**;
- Weighted path capacities;
- MPX transport key;
- background-resident preference.

A Bundle contains 1–32 Profiles:

- single_select: exactly one Profile runs;
- parallel: one or more Profiles may run at the same time.

Parallel startup performs an **atomic local listen-port preflight**. A local configuration error such as duplicate/occupied ports blocks the group. After that preflight, runtime failures are isolated: an unreachable or authentication-failed Profile is reported separately while healthy Profile listeners continue running.

The macOS client stores the secret Provisioning URL in Keychain. Linux can use validate-managed / run-managed with the URL supplied on stdin so the bearer credential does not need to appear in process arguments.

## Diagnostics

MPTCP Desk exposes current Session and path state rather than only a single aggregate speed number.

Always-visible diagnostics include:

- configured/effective scheduler and mode switches;
- active TCP paths and logical connections;
- upload/download counters;
- current/peak reorder bytes and pending reliable data;
- TCP retransmits and UDP drop/timeout events;
- per-path RTT, measured goodput, queue, outstanding bytes and path errors.

Deep resource information is available through two default-collapsed panels:

- **Stream / Lifecycle resources**
- **Window / Credit resources**

For a remote Bundle, every Profile keeps an independent diagnostic state. Expanding one Profile does not affect another.

## Current implementation bounds

The current v0.10.7 implementation uses:

- **2–8 configured Relays per Profile**;
- **up to 8 MPX/4 Carriers per Session**;
- up to **2048 active peer-initiated Streams**;
- **32 KiB** maximum STREAM_DATA payload;
- **16 MiB** maximum per-Stream receive-credit window;
- **128 MiB** Session receive-credit window;
- **128 MiB** physical receive-page accounting bound;
- bounded sender DATA/control queues.

These are implementation limits, not claims about the maximum encodable value of every MPX/4 field.

## Scheduler modes

- **Auto** — starts from aggregate behavior and can enter protection behavior when stable evidence shows a degraded path.
- **Aggregate** — schedules ordinary traffic over multiple eligible Carriers using live path cost.
- **Protect** — keeps healthy paths active while degraded paths are restricted to bounded probing/recovery behavior.
- **Weighted** — adds configured directional capacity to the same live path-cost and safety signals.

download_mbps is required for Weighted. upload_mbps is optional; an omitted uplink capacity can be estimated locally. Configured capacity is a scheduler input, not flow-control credit and not a guaranteed delivery rate.

## Compatibility

The supported release suite is **0.10.7 Client + 0.10.7 Landing + 0.10.7 Provisioning**.

v0.10.7 keeps the exact MPX/4 Draft 04 data plane, Carrier Generation/Error Scope, scheduler, flow-control and key-schedule semantics used by v0.10.5. This patch release refreshes the macOS and Provisioning interfaces; the signed updater and remote device-control behavior introduced in v0.10.6 remain unchanged. v0.10.7 clients continue to accept legacy plaintext schema-1/schema-2 Provisioning responses for migration, while 0.10.2+ Provisioning normally returns the opaque encrypted envelope.

Pre-MPX/4 releases are retained in the repository history and historical documents, but they are not the current deployment target.

## Security model

MPX/4 Draft 04 uses a 32-byte pre-shared transport key as the authentication root, HKDF-SHA256/HMAC-SHA256 for key derivation and Finished authentication, and AES-256-GCM for Secure Records. Every authenticated Carrier derives fresh directional traffic keys and IVs.

The Provisioning public-response envelope uses the existing high-entropy URL secret as key material and does **not** add device enrollment or a second credential. Possession of the complete Provisioning URL therefore still grants configuration access. Use HTTPS for remote Provisioning and treat both the URL and transport key as credentials.

This is not TLS PKI, and Draft 04 does not provide forward secrecy. The distributed macOS app is ad-hoc signed and is not Developer ID notarized.

## Quick start

- [Quick Start](docs/guides/QUICKSTART.md)
- [快速开始](docs/guides/QUICKSTART.zh-CN.md)
- [Architecture / 架构](docs/guides/ARCHITECTURE.zh-CN.md)
- [Troubleshooting / 故障排查](docs/guides/TROUBLESHOOTING.zh-CN.md)

## Build from source

~~~sh
# Go engine / Landing tests
cd macos/engine
go test ./...

# macOS Universal DMG
MPTCP_GO=/path/to/go ./macos/build.sh

# Linux amd64 + arm64 Client
MPTCP_GO=/path/to/go ./scripts/build-linux-client.sh

# Linux amd64 + arm64 Landing
MPTCP_GO=/path/to/go ./scripts/build-userspace-landing.sh

# Linux amd64 + arm64 Provisioning
MPTCP_GO=/path/to/go ./scripts/build-provisioning.sh
~~~

See [Building from source](docs/guides/BUILDING.md) for the full build workflow.

## Documentation

- [MPX/4 Draft 04 implementation profile](docs/userspace/PROTOCOL.md)
- [Provisioning Profiles, Bundles and encrypted responses](docs/userspace/PROVISIONING.md)
- [Linux headless client](docs/userspace/LINUX-CLIENT.md)
- [Scheduler modes](docs/userspace/SCHEDULER-MODES.md)
- [Deployment and rollback](docs/userspace/DEPLOYMENT.zh-CN.md)
- [Validation and release limits](docs/userspace/VALIDATION.md)
- [v0.10.7 release notes](docs/userspace/RELEASE.zh-CN.md)

Version-specific MPX/2 and MPX/3 documents are retained for historical/implementation archaeology and are not the current protocol guide.
