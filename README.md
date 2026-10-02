# MPTCP Userspace

MPTCP Userspace is an application-layer multipath transport for macOS and Linux. It combines multiple ordinary TCP carrier connections into one authenticated MPX Session and multiplexes application TCP streams across those carriers.

It is **not kernel MPTCP** and it is **not QUIC**. The macOS client uses ordinary TCP carrier sockets; the Linux Landing terminates MPX/4 and forwards opaque backend TCP bytes.

Current release: **v0.9.6 / MPX/4 Draft 03**.

Next candidate: **v0.9.7** adds full managed client provisioning: one secret API URL can supply the complete Mac runtime profile, backed by the included self-hosted Provisioning Server and web console.

- Release: https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.9.6
- MPX/4 specification: https://github.com/Dsd1001/MPX-4
- Chinese README: [README.zh-CN.md](README.zh-CN.md)

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
```

Relay nodes only forward ordinary TCP bytes. MPX/4 authentication, Secure Records, Stream multiplexing, flow control, scheduling, retransmission and cross-Carrier reinjection are end-to-end between MPTCP Desk and Landing.

## v0.9.6: MPX/4 Draft 03

The TCP userspace data plane now implements the public MPX/4 Draft 03 specification:

- canonical MPX VarInt encoding;
- `CLIENT_INIT / SERVER_INIT / CLIENT_FINISHED / SERVER_FINISHED` handshake;
- PSK authentication rooted in a 32-byte transport key;
- HKDF-SHA256 key schedule and HMAC-SHA256 Finished verification;
- AES-256-GCM Secure Records with independent directional sequence spaces;
- typed MPX/4 Frames and record batching;
- authenticated Session CREATE and Carrier JOIN;
- Carrier ID plus monotonically increasing Carrier Generation on replacement;
- reliable ordered Streams with explicit Stream and Session credit;
- retransmission and cross-Carrier reinjection using stable Transmission IDs;
- Auto, Aggregate, Protect and Weighted scheduler negotiation;
- same-Carrier delivery feedback using receiver timestamps in microseconds.

The implementation is checked against the Draft 03 repository test vectors for VarInt, Frame encoding, key schedule and consecutive Secure Records.

## Scheduler modes

The existing scheduler implementation is retained above the new MPX/4 wire layer:

- **Auto** — learns path behavior and can protect against materially inferior paths.
- **Aggregate** — concurrently schedules eligible work across carriers.
- **Protect** — favors the active path set while keeping alternate protection paths available.
- **Weighted** — combines configured carrier capacity with live RTT, queue, penalty, timeout and delivery signals.

For Weighted, `download_mbps` is required and `upload_mbps` is optional. Capacity is encoded as the MPX/4 `PATH_CAPACITY` parameter in 100,000 bit/s units.

## Compatibility

v0.9.6 changes the TCP wire protocol from MPX/3 to MPX/4 and therefore requires **0.9.6 on both MPTCP Desk and Landing**.

| MPTCP Desk | Landing | TCP userspace |
|---|---|---|
| 0.9.6 | 0.9.6 | Yes — MPX/4 Draft 03 |
| 0.9.6 | 0.9.5 or older | No |
| 0.9.5 or older | 0.9.6 | No |

Existing profiles can keep the same Relay addresses, ports, scheduler selection and 32-byte transport key, but both protocol endpoints must be upgraded together.

UDP remains an **independent MPU/1 datagram data plane** in 0.9.6. It is not represented as an MPX/4 Core Datagram extension and does not use Weighted capacity values.

## Resource model

The mature 0.9.5 resource boundaries remain in place:

- up to 8 carriers per Session;
- up to 2048 active peer-initiated Streams;
- 32 KiB maximum STREAM_DATA payload;
- 16 MiB maximum per-Stream receive-credit window;
- 128 MiB Session receive-credit window;
- bounded sender pending data and control queues;
- bounded 128 MiB physical receive-page accounting.

Carrier loss does not terminate the logical Session while another carrier remains usable. Outstanding reliable Transmissions return to the Session scheduler for retransmission/reinjection. A replacement TCP Carrier performs a complete MPX/4 JOIN handshake with a greater Carrier Generation and fresh traffic keys.

## macOS client

MPTCP Desk supports macOS 13+ on arm64 and x86_64. The optional background-resident behavior introduced in 0.9.5 is retained: login-item registration, sleep/wake recovery, network availability monitoring and bounded restart backoff.

The default userspace TCP entry is `127.0.0.1:1081`. It is a transparent local TCP entry, **not a SOCKS5 server**.

## Security

MPX/4 Draft 03 uses a 32-byte pre-shared transport key as the authentication root. Each authenticated Carrier performs a fresh handshake and derives independent Client-to-Server and Server-to-Client application traffic keys and IVs.

This is not TLS PKI and v0.9.6 does not provide forward secrecy. Transport keys must not be committed to the repository or exposed in logs. The distributed macOS application is ad-hoc signed and is not Developer ID notarized.

## Build

```sh
# Go tests
cd macos/engine
go test ./...

# macOS Universal DMG
MPTCP_GO=/path/to/go ./macos/build.sh

# Linux amd64 Landing
MPTCP_GO=/path/to/go ./scripts/build-userspace-landing.sh
```

## Documentation

- [MPX/4 implementation profile](docs/userspace/PROTOCOL.md)
- [v0.9.6 release notes](docs/userspace/RELEASE.zh-CN.md)
- [Scheduler modes](docs/userspace/SCHEDULER-MODES.md)
- [Deployment and rollback](docs/userspace/DEPLOYMENT.zh-CN.md)
- [Quick start](docs/guides/QUICKSTART.zh-CN.md)
- [Troubleshooting](docs/guides/TROUBLESHOOTING.zh-CN.md)
- [Managed client provisioning](docs/userspace/PROVISIONING.md)
