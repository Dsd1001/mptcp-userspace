# MPTCP Userspace

[中文说明](README.zh-CN.md)

MPTCP Userspace is an application-layer multipath transport for macOS and Linux. It combines multiple ordinary TCP carrier paths into one authenticated MPX/3 session and multiplexes application TCP streams across those paths.

It is **not kernel MPTCP** and it is **not QUIC**. The macOS client explicitly uses ordinary TCP carriers; the Linux Landing terminates MPX/3 and forwards opaque backend TCP bytes.

Current release: **v0.9.4 / MPX/3 capability revision 5**.

- Release: https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.9.4
- macOS: Universal arm64/x86_64 DMG, macOS 13+
- Landing: Linux amd64 static binary
- Scheduler modes: Auto, Aggregate, Protect, Weighted

## What it does

A typical deployment looks like this:

~~~text
Application / Surge
        |
        v
127.0.0.1:1081
(transparent TCP entry, not SOCKS5)
        |
        v
MPTCP Desk / Userspace engine
        |
        +---- ordinary TCP carrier ---- Relay A ----+
        +---- ordinary TCP carrier ---- Relay B ----+---- Linux Landing ---- backend
        +---- ordinary TCP carrier ---- Relay C ----+
        +---- ...
~~~

Relays only forward ordinary TCP bytes. MPX/3 authentication, stream multiplexing, scheduling, credit control, retransmission and reinjection are handled by the macOS engine and Landing.

## Scheduler modes

| Mode | Purpose | Capacity source |
|---|---|---|
| **Auto** | Default. Dynamically stays in Aggregate on healthy homogeneous paths and moves toward Protect behavior when path quality diverges or delivery fails. | Learned |
| **Aggregate** | Throughput-oriented scheduling for paths with similar quality. | Learned |
| **Protect** | Keeps unhealthy or strongly inferior paths from accumulating excessive DATA debt; degraded paths can become probe/backup paths. | Learned |
| **Weighted** | For fixed path groups whose usable capacity is already known. Each Relay gets a required downstream capacity and an optional upstream capacity. | Configured capacity, with live safety signals still enforced |

Weighted is not a blind static ratio splitter. Live RTT, writer queue pressure, disconnect state, path penalties, delivery timeout, reinjection and retransmission still affect path selection.

For Weighted:

- download_mbps is required per Relay and is used by Landing → Mac.
- upload_mbps is optional and is used by Mac → Landing.
- If upload capacity is omitted, only Mac → Landing falls back to learned Aggregate capacity.
- Supported configured range: 0.1–6553.5 Mbps, 0.1 Mbps precision.
- Weighted requires **0.9.4 on both Mac and Landing**.

## Compatibility

| Mac | Landing | Auto / Aggregate / Protect | Weighted |
|---|---|---:|---:|
| 0.9.4 | 0.9.4 | Yes | Yes |
| 0.9.4 | 0.9.3 | Yes | No |
| 0.9.3 | 0.9.4 | Yes | No |
| MPX/2 / older candidates | MPX/3 Rev5 | No | No |

0.9.4 keeps the 0.9.3 scheduler hello values 0x41, 0x42, 0x43 for Auto/Aggregate/Protect. Weighted uses 0x44 and authenticated directional capacity fields.

## Resource boundaries

v0.9.4 does not increase the established resource limits:

- up to 2048 occupied logical stream identities;
- 128 MiB session credit;
- 128 MiB sender DATA pending;
- 128 MiB physical receive page accounting;
- up to 16 MiB per-stream receive window;
- DATA payload up to 32 KiB.

See [MPX/3 credit control](docs/userspace/MPX3-CREDIT.md) and [protocol](docs/userspace/PROTOCOL.md) for details.

## Quick start

See:

- [Quick Start](docs/guides/QUICKSTART.md)
- [Build from source](docs/guides/BUILDING.md)
- [快速开始](docs/guides/QUICKSTART.zh-CN.md)
- [Deployment and rollback](docs/userspace/DEPLOYMENT.zh-CN.md)
- [Architecture](docs/guides/ARCHITECTURE.zh-CN.md)

For an existing managed Landing, a controlled binary upgrade can use:

~~~sh
chmod 755 ./mptcp-landing
./mptcp-landing version
./mptcp-landing upgrade --source ./mptcp-landing --sha256 <trusted-full-sha256>
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
~~~

The Landing manager retains the previous binary/config pair so mptcp-landing rollback can restore it.

## Security model

MPX/3 uses an authenticated PSK handshake and independent AES-GCM keys/counters per direction. Scheduler mode and Weighted capacity fields are included in the authenticated handshake transcript.

Important limits:

- this is not TLS PKI;
- v0.9.4 does not provide forward secrecy;
- no independent security certification is claimed;
- transport keys must not be committed to repositories or pasted into logs/issues;
- the macOS DMG is ad-hoc signed and **not notarized**.

## Validation scope

The v0.9.4 release passed source-matched correctness, authenticated directional capacity, failure-protection and laboratory Weighted high-BDP gates.

The release does **not** claim that the complete 30-second capacity matrix or a physical 180-second App+Surge/WAN acceptance run was completed for this Weighted release. Exact evidence and limitations are in the release assets:

- ACCEPTANCE.md
- TESTS.json
- SCHEDULER-MODES.json
- PROVENANCE.json

Laboratory results are not a guarantee of ISP bandwidth, arbitrary WAN throughput or multi-day stability.

## Release provenance

The v0.9.4 tag points to the frozen source commit used for the published release assets:

~~~text
Commit:    f97f810b393c5f67dca02607ccefb41d78c7c169
Source-ID: d4b8f362a8179257f2889abfea587c480755470bac6a78d8c023c3496c66f2bf
~~~

The default main branch may contain documentation-only commits after the release tag. Use the tag and Source-ID when reproducing or auditing the released binaries.

## Documentation

Start at [docs/README.md](docs/README.md).

Key documents:

- [Protocol](docs/userspace/PROTOCOL.md)
- [Scheduler modes](docs/userspace/SCHEDULER-MODES.md)
- [Credit control](docs/userspace/MPX3-CREDIT.md)
- [Deployment](docs/userspace/DEPLOYMENT.zh-CN.md)
- [Validation](docs/userspace/VALIDATION.md)
- [Troubleshooting](docs/guides/TROUBLESHOOTING.zh-CN.md)
- [v0.9.4 release notes](docs/userspace/RELEASE.zh-CN.md)
