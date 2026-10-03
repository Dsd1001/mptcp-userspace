# MPTCP Userspace

MPTCP Userspace is an application-layer multipath transport for macOS and Linux. It combines multiple ordinary TCP carrier connections into one authenticated MPX/4 Session and multiplexes application TCP streams across those Carriers.

It is **not kernel MPTCP** and it is **not QUIC**. The macOS and Linux Userspace clients use ordinary TCP carrier sockets; the Linux Landing terminates MPX/4 and forwards opaque backend TCP bytes.

Current transport release: **v0.9.8 / MPX/4 Draft 04**. Current standalone Provisioning release: **v0.9.9**.

- Release: https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.9.8
- MPX/4 specification: https://github.com/Dsd1001/MPX-4
- Chinese README: [README.zh-CN.md](README.zh-CN.md)

## Published platforms

| Component | OS / architecture | Artifact |
|---|---|---|
| MPTCP Desk client | macOS arm64 + x86_64 | `MPTCP-Desk-0.9.8-universal.dmg` |
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
        '-- HTTPS secret profile URL --> MPTCP Desk
```

Relay nodes only forward ordinary TCP bytes. MPX/4 authentication, Secure Records, Stream multiplexing, flow control, scheduling, retransmission and cross-Carrier reinjection are end-to-end between MPTCP Desk and Landing.

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

v0.9.8 formally includes the self-hosted Provisioning platform. A Mac can store one secret HTTPS API URL and obtain its complete runtime profile before starting:

- Userspace MPX/4 or Native mode;
- local listen port;
- TCP / UDP switches;
- Auto / Aggregate / Protect / Weighted scheduler;
- Relay IPv4/port list;
- Weighted download/upload capacities;
- 32-byte MPX transport key;
- background-resident setting.

The repository includes the Linux `mpx-provision` service, embedded administration web console, Docker deployment example and macOS managed-mode client. The API URL and transport key are stored in Keychain on macOS; managed mode does not silently start from stale cached configuration if the authoritative API fetch fails.

## Scheduler modes

- **Auto** — selects a local operating policy from observed Session and Carrier state.
- **Aggregate** — concurrently schedules ordinary traffic over multiple eligible Carriers.
- **Protect** — may prefer one or more Carriers while retaining alternates for protection, retransmission and recovery.
- **Weighted** — uses negotiated `PATH_CAPACITY` together with live usability, RTT, queue, penalty and delivery signals.

For Weighted, `download_mbps` is required and `upload_mbps` is optional. Capacity units are 100,000 bit/s. Configured capacity is a scheduling input, not flow-control credit or a guaranteed delivery rate.

## Compatibility

MPX/4 Draft 04 preserves the Draft 03 wire encoding, but v0.9.8 also fixes and enforces the Draft 04 Generation and Error Code semantics. The supported release pairing is therefore **0.9.8 on both MPTCP Desk and Landing**.

| MPTCP Desk | Landing | Status |
|---|---|---|
| 0.9.8 | 0.9.8 | Supported — MPX/4 Draft 04 |
| 0.9.8 | 0.9.6 / 0.9.7-derived build | Same version-4 byte format, but mixed semantic behavior is not a supported release pairing |
| 0.9.8 | 0.9.5 or older | Incompatible — MPX/3 |

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
- [v0.9.8 release notes](docs/userspace/RELEASE.zh-CN.md)
- [Linux headless client](docs/userspace/LINUX-CLIENT.md)
- [Managed client provisioning](docs/userspace/PROVISIONING.md)
- [Scheduler modes](docs/userspace/SCHEDULER-MODES.md)
- [Deployment and rollback](docs/userspace/DEPLOYMENT.zh-CN.md)
- [Quick start](docs/guides/QUICKSTART.zh-CN.md)
- [Troubleshooting](docs/guides/TROUBLESHOOTING.zh-CN.md)
