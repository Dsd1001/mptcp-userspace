# MPTCP Userspace

MPTCP Userspace is an application-layer multipath transport for macOS and Linux. It combines multiple ordinary TCP carrier connections into one authenticated MPX/4 Session and multiplexes application TCP streams across those Carriers.

It is **not kernel MPTCP** and it is **not QUIC**. The macOS and Linux Userspace clients use ordinary TCP carrier sockets; the Linux Landing terminates MPX/4 and forwards opaque backend TCP bytes.

Current suite release: **v0.10.0 / MPX/4 Draft 04 + multi-profile Provisioning Bundles**.

- Release: https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.10.0
- MPX/4 specification: https://github.com/Dsd1001/MPX-4
- Chinese README: [README.zh-CN.md](README.zh-CN.md)

## Published platforms

| Component | OS / architecture | Artifact |
|---|---|---|
| MPTCP Desk client | macOS arm64 + x86_64 | `MPTCP-Desk-0.10.0-universal.dmg` |
| Headless client | Linux amd64 | `mptcp-client-linux-amd64` |
| Headless client | Linux arm64 | `mptcp-client-linux-arm64` |
| Landing | Linux amd64 | `mptcp-landing` |
| Landing | Linux arm64 | `mptcp-landing-linux-arm64` |
| Provisioning | Linux amd64 | `mpx-provision` |
| Provisioning | Linux arm64 | `mpx-provision-linux-arm64` |

The Linux client supports `userspace_multipath`; Native MPTCP fallback remains macOS-only.

## Architecture

```text
Application / Surge
        |
        v
127.0.0.1:1081 transparent TCP entry
        |
        v
MPTCP Desk / userspace engine
        |
        +-- ordinary TCP Carrier 1 --> Relay A --+
        +-- ordinary TCP Carrier 2 --> Relay B --+--> Linux Landing --> backend
        '-- ordinary TCP Carrier N --> Relay N --+
                   MPX/4 Session

Provisioning web console
        |
        +-- secret Profile URL -----------------> one runtime Profile
        '-- secret Bundle URL --> Profile A/B/C --> manual selection or parallel Sessions
```

Relay nodes only forward ordinary TCP bytes. MPX/4 authentication, Secure Records, Stream multiplexing, flow control, scheduling, retransmission and cross-Carrier reinjection are end-to-end between MPTCP Desk and Landing.

## v0.10.0: Provisioning Bundles and multi-profile client

v0.10.0 keeps the MPX/4 Draft 04 wire protocol and scheduler semantics unchanged while adding a control-plane/runtime orchestration layer above independent MPX Sessions. Provisioning now has two first-class objects:

- **Profile** — one complete runtime configuration with its own `listen_port`, Relay set, scheduler, transport key and TCP/UDP switches;
- **Bundle** — an ordered set of Profiles exposed through one independent secret API URL.

A Bundle can operate in **single-select** mode, where the client manually chooses exactly one Profile, or **parallel** mode, where one or more selected Profiles run simultaneously as independent Sessions/listeners. Profiles are never flattened into one Relay pool. In parallel mode Provisioning refuses duplicate `listen_port` values and the client repeats the port-conflict check before starting any Profile. The listen port remains authoritative Provisioning data.

MPTCP Desk can fetch Bundle schema 2, remember the local selection for that Bundle and start the selected Profile(s). The Linux client supports the same Bundle validation/runtime model through `validate-bundle` / `run-bundle`, and can fetch Profile or Bundle URLs through the stdin-only `validate-managed` / `run-managed` control command so the secret URL does not need to appear in process arguments.

Existing schema-1 `/v1/config/...` URLs remain supported. Bundle endpoints use `/v1/bundle/...` and retain the same high-entropy bearer-secret model, including optional readable aliases.

## v0.9.8: MPX/4 Draft 04

v0.9.8 follows MPX/4 **Draft 04**, based on specification commit `5854899b63676eb8bb43048678ef99b4589170c3`.

Draft 04 intentionally keeps the Draft 03 byte encodings but tightens state semantics. v0.9.8 therefore adds the normative behavior that was not explicit in the previous implementation:

- Highest Accepted Generation is retained for every used Carrier ID for the whole Session;
- the first accepted incarnation of a Carrier ID is Generation 0;
- stale or equal Carrier Generation reuse is rejected with `CARRIER_CONFLICT`, including after transport loss;
- a higher Generation is committed only after authenticated Carrier establishment;
- committing a replacement supersedes lower Generations and prevents them from creating new protocol state or receiving new Attempts;
- Carrier Generation never wraps;
- reliable Transmission IDs survive retransmission, reinjection and Carrier replacement;
- never-allocated Transmission acknowledgements are distinguished from harmless stale/settled duplicates;
- `STREAM_OPEN_REJECT`, `CARRIER_CLOSE` and `SESSION_CLOSE` use the MPX/4 Error Code registry and Draft 04 failure scopes;
- flow-control, final-size, Transmission-ID and established Stream-state failures close the Session where required;
- malformed authenticated Frame encoding is Carrier-scoped; authentication/integrity failure terminates only that Carrier;
- scheduler IDs retain their Draft 04 semantic contracts while the concrete scheduling algorithm remains implementation-defined.

The repository vendors the official Draft 04 `carrier-generation.json` and `error-scope.json` semantic vectors in addition to the existing byte-level VarInt, Frame, key-schedule and Secure-Record tests.

## Managed Provisioning

Provisioning can issue either a single Profile URL or a Bundle URL. A Profile still contains the full authoritative runtime configuration: transport mode, local listen port, TCP/UDP switches, scheduler, Relay list/capacities, transport key and background-resident setting. A Bundle returns multiple complete Profiles in one schema-2 document.

For `single_select`, different Profiles may reuse the same listen port because only one can be active. For `parallel`, all Profiles in the Bundle must use different listen ports. Provisioning validates this when the Bundle is saved and also blocks later Profile edits that would make an existing parallel Bundle invalid. The client validates again before startup and probes the required local sockets before spawning any Profile runtime.

Each simultaneously active Profile gets an independent MPX Session and its own Carrier set; Bundle orchestration does not change MPX/4 data-plane bytes. Existing schema-1 Profile URLs and 0.9.9 Provisioning records remain readable after upgrade.

## Scheduler modes

- **Auto** — selects a local operating policy from observed Session and Carrier state.
- **Aggregate** — concurrently schedules ordinary traffic over multiple eligible Carriers.
- **Protect** — may prefer one or more Carriers while retaining alternates for protection, retransmission and recovery.
- **Weighted** — uses negotiated `PATH_CAPACITY` together with live usability, RTT, queue, penalty and delivery signals.

For Weighted, `download_mbps` is required and `upload_mbps` is optional. Capacity units are 100,000 bit/s. Configured capacity is a scheduling input, not flow-control credit or a guaranteed delivery rate.

## Compatibility

v0.10.0 keeps the same MPX/4 Draft 04 wire encoding and normative state semantics as v0.9.8; the release change is primarily Provisioning/client orchestration. The published and tested suite pairing is nevertheless **0.10.0 client + 0.10.0 Landing + 0.10.0 Provisioning**. Mixed 0.9.8/0.10.0 data-plane binaries are not the release-tested configuration even though their MPX/4 wire semantics are intentionally unchanged.

| Client | Landing | Status |
|---|---|---|
| 0.10.0 | 0.10.0 | Supported release pairing — MPX/4 Draft 04 |
| 0.10.0 | 0.9.8 | Same Draft 04 data-plane semantics, but not the tested 0.10.0 suite pairing |
| 0.10.0 | 0.9.5 or older | Incompatible — MPX/3 |

Existing Relay addresses, ports, scheduler choices and 64-hex-character transport keys can be reused when both endpoints are upgraded.

UDP remains an **independent MPU/1 datagram data plane**. Draft 04 does not define the project UDP mode as an MPX/4 Core Datagram extension, and MPX/4 Weighted capacities are not applied to MPU/1.

## Resource model

The established hard bounds remain in place:

- up to 8 Carriers per Session;
- up to 2048 active peer-initiated Streams;
- 32 KiB maximum STREAM_DATA payload;
- 16 MiB maximum per-Stream receive-credit window;
- 128 MiB Session receive-credit window;
- bounded sender data/control queues;
- bounded 128 MiB physical receive-page accounting.

Carrier loss does not itself terminate Stream state. Outstanding reliable Transmissions return to the Session scheduler and retain their Transmission IDs when retransmitted or reinjected.

## Security

MPX/4 Draft 04 uses a 32-byte pre-shared transport key as the authentication root, HKDF-SHA256/HMAC-SHA256 for key derivation and Finished authentication, and AES-256-GCM for Secure Records. Each authenticated Carrier derives fresh directional traffic keys and IVs.

This is not TLS PKI and the protocol does not provide forward secrecy in Draft 04. Transport keys and Provisioning API URLs are credentials and must not be committed to the repository or ordinary logs. The distributed macOS app is ad-hoc signed and not Developer ID notarized.

## Build

```sh
# Go engine / Landing tests
cd macos/engine
go test ./...

# macOS Universal DMG
MPTCP_GO=/path/to/go ./macos/build.sh

# Linux amd64 + arm64 headless client
MPTCP_GO=/path/to/go ./scripts/build-linux-client.sh

# Linux amd64 + arm64 Landing
MPTCP_GO=/path/to/go ./scripts/build-userspace-landing.sh

# Linux amd64 + arm64 Provisioning service
MPTCP_GO=/path/to/go ./scripts/build-provisioning.sh
```

## Documentation

- [MPX/4 Draft 04 implementation profile](docs/userspace/PROTOCOL.md)
- [v0.10.0 release notes](docs/userspace/RELEASE.zh-CN.md)
- [Linux headless client](docs/userspace/LINUX-CLIENT.md)
- [Managed client provisioning](docs/userspace/PROVISIONING.md)
- [Scheduler modes](docs/userspace/SCHEDULER-MODES.md)
- [Deployment and rollback](docs/userspace/DEPLOYMENT.zh-CN.md)
- [Quick start](docs/guides/QUICKSTART.zh-CN.md)
- [Troubleshooting](docs/guides/TROUBLESHOOTING.zh-CN.md)
