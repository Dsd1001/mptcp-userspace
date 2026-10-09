# MPTCP Userspace

MPTCP Userspace is an application-layer multipath transport for macOS and Linux. It combines multiple ordinary TCP Carrier connections into one authenticated **MPX/4 Protocol Version 4 Stable** Session, multiplexes many application Streams over those Carriers, and can retransmit or reinject reliable data when a path slows down or disappears.

Current release: **v1.1.3**
Protocol source: **MPX/4 `protocol-v4.0.0`** (`44f587fd279ed2238b070dd68114c76822353f4d`)

It is **not kernel MPTCP** and it is **not QUIC**. Relay nodes forward opaque Carrier bytes and do not need the MPX Transport Key. MPX authentication, encryption, Stream state, flow control, scheduling and reinjection are end-to-end between the Client and Landing.

[中文说明](README.zh-CN.md) · [Documentation](docs/README.md) · [Latest release](https://github.com/Dsd1001/mptcp-userspace/releases/tag/v1.1.3) · [MPX/4 specification](https://github.com/Dsd1001/MPX-4)

## Recommended production topology

```text
Application
    |
    v
MPTCP Desk (macOS / Windows) / Linux Client
    |
    |  multiple ordinary TCP Carriers
    +--------> Relay A --+
    +--------> Relay B --+--> Landing --> Backend TCP/UDP service
    +--------> Relay C --+
```

For the current implementation, the recommended Linux TCP congestion-control baseline is:

- **Landing: CUBIC**
- **Relay: BBR** (prefer `fq` as the host qdisc)

This is a deployment recommendation, not an MPX/4 wire requirement. The split has proved more predictable for this architecture: Relay hosts benefit from BBR pacing and queue control on heterogeneous Carrier legs, while the convergence endpoint is kept on CUBIC so MPX/4's own multipath scheduler and flight control are not stacked on another model-based controller at Landing.

Do not blindly apply the same congestion controller to every host. See [Network tuning](docs/guides/NETWORK-TUNING.md) for verification, persistent sysctl examples and rollback guidance.

## Components

| Component | Purpose |
| --- | --- |
| **MPTCP Desk** | macOS and Windows GUI clients sharing the MPX/4 Userspace engine. Windows is userspace-only; both provide local/managed profiles, diagnostics, background recovery, updates and optional remote device control. |
| **Linux Client** | Headless client using the same MPX/4 engine as MPTCP Desk. |
| **Relay** | Opaque TCP forwarder between Client and Landing. It is not an MPX endpoint and does not need the Transport Key. |
| **Landing** | MPX/4 server endpoint. Terminates Carrier Sessions and forwards Streams to the configured backend. |
| **Provisioning** | Optional HTTPS configuration and remote-control service for Profiles, Bundles and managed devices. |

## What's new in v1.1.2

- Sender-side Queue-aware DATA Admission defaults to 32 MiB of not-yet-scheduled DATA per Session, with 4 MiB extra new-Stream room. The environment override MPX_QUEUE_ADMISSION_MIB=0 disables it for A/B or rollback. Stream and Session peer WINDOWs remain authoritative.
- macOS now supports Userspace only, like Windows and Linux. Native/kernel MPTCP options, privileged sysctl actions and the native socket implementation have been removed; old Native profiles must be deliberately reconfigured with a valid MPX/4 Transport Key.
- macOS, Windows, Linux Client, Landing and Provisioning versions are aligned at v1.1.2.
- The controlled six-path/150-Stream shaped-loopback A/B passed; this is not proof of WAN throughput improvement.

## v1.1.1 flow-control changes

v1.1.1 keeps the v1.1.0 concurrent data-plane refactor and simplifies send-side flow control. The peer's MPX/4 Stream and Session WINDOWs are now the authoritative send-credit gates. Legacy local `txUsed` / `txGrowth` accounting remains for diagnostics but no longer adds a second 128 MiB send-admission barrier.

The stable protocol limits remain unchanged:

- up to **8 active Carriers** in this implementation;
- up to **2048 Streams**;
- **32 KiB** maximum STREAM_DATA payload;
- **16 MiB** maximum per-Stream receive window;
- **128 MiB** Session receive-credit limit;
- **128 MiB** physical receive-buffer accounting;
- **1 GiB** DATA pending-byte pool;
- cross-Carrier retransmission/reinjection with Session-wide Transmission IDs.

The Carrier ID namespace itself is not limited to eight IDs; eight is the current local active-Carrier implementation limit.

## Scheduler modes

The implementation provides four local scheduling policies:

- **Auto** — general-purpose policy that adapts to path health and load.
- **Aggregate** — favors using multiple eligible Carriers concurrently.
- **Protect** — limits unhealthy paths and keeps bounded probing for recovery.
- **Weighted** — combines configured directional capacity with live RTT, queue, delivery and penalty signals.

Scheduler mode is local endpoint policy, not an MPX/4 Stable Core negotiation value. Client and Landing can use different policies, although matching them is usually easier to operate. Weighted capacity is a prior, not a fixed traffic percentage or bandwidth guarantee.

## TCP, UDP and UoT

Application TCP uses MPX/4 Streams over authenticated TCP Carriers.

UDP has two product paths:

- **native UDP** via the authenticated MPU/1 data plane;
- **UoT**, which transports UDP payload through the authenticated TCP Carrier Session.

TCP congestion-control recommendations affect TCP Carrier/UoT transport and ordinary backend TCP sockets. Native UDP is not governed by Linux TCP congestion control.

## Quick start

1. Download the v1.1.1 artifacts and verify `MPTCP-Userspace-1.1.1-SHA256SUMS`.
2. Install Landing and configure the backend, Transport Key, scheduler and session limit.
3. Set **Landing to CUBIC**.
4. Configure each Relay to forward its Carrier port to Landing and set **Relay to BBR**.
5. Create a local Profile or managed Provisioning URL in MPTCP Desk (macOS / Windows) / Linux Client.
6. Start the client and verify that multiple Carriers become connected.

Detailed instructions: [Quick Start](docs/guides/QUICKSTART.md).

## Release artifacts

The v1.1.1 release publishes:

```text
MPTCP-Desk-1.1.1-universal.dmg
mptcp-client-linux-amd64
mptcp-client-linux-arm64
mptcp-landing
mptcp-landing-linux-arm64
mpx-provision
mpx-provision-linux-arm64
MPTCP-Userspace-1.1.1-SHA256SUMS
MPTCP-Userspace-1.1.1-source.tar.gz
MPTCP-Userspace-1.1.1-release.tar.gz
PROVENANCE.json
TESTS.json
RUNTIME.json
CAPACITY.json
SCHEDULER-MODES.json
ACCEPTANCE.md
```

v1.1.1 passed correctness/build/concurrency regression gates. The release did **not** claim a new physical-WAN throughput or capacity benchmark; production performance must still be verified on the actual Relay/Landing paths.

## Documentation

Start with:

- [Quick Start](docs/guides/QUICKSTART.md)
- [Network tuning: Landing CUBIC / Relay BBR](docs/guides/NETWORK-TUNING.md)
- [Architecture](docs/guides/ARCHITECTURE.zh-CN.md)
- [Deployment and rollback](docs/userspace/DEPLOYMENT.zh-CN.md)
- [Troubleshooting](docs/guides/TROUBLESHOOTING.zh-CN.md)
- [MPX/4 implementation profile](docs/userspace/PROTOCOL.md)
- [Scheduler modes](docs/userspace/SCHEDULER-MODES.md)
- [Linux Client](docs/userspace/LINUX-CLIENT.md)
- [Provisioning](docs/userspace/PROVISIONING.md)
- [v1.1.1 release notes](docs/userspace/RELEASE.zh-CN.md)

Historical MPX/3 and pre-Stable documents are retained only for implementation archaeology and are clearly marked as historical in the documentation index.

## Security notes

- Treat the 64-hex-character Transport Key as a secret.
- Treat a full managed Provisioning URL as a bearer credential.
- Use HTTPS for non-loopback Provisioning.
- Do not log Transport Keys, Authorization headers or complete secret URLs.
- Landing private configuration must remain `0600`/`0400` or be delivered through the managed systemd credential path.

## Scope

MPTCP Userspace provides an application-layer multipath transport. It does not automatically tune every network hop, remove ISP congestion, or make aggregate throughput equal to the arithmetic sum of configured links. Carrier quality, RTT, loss, Relay CPU, queueing, backend capacity and Linux congestion-control choices all remain part of the end-to-end system.
