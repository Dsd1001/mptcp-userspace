# MPX/4 implementation profile — MPTCP Userspace v1.1.1

This document describes the MPX/4 behavior implemented by MPTCP Userspace v1.1.1. The normative protocol source is the separate MPX/4 repository release:

```text
Protocol release: protocol-v4.0.0
Protocol source:  44f587fd279ed2238b070dd68114c76822353f4d
Wire version:     4
Capability rev:   8
```

When this document and the normative MPX/4 protocol text differ, the normative protocol text and the v1.1.1 source are authoritative.

## 1. Scope

MPX/4 is an authenticated application-layer multipath transport. It runs over ordinary byte-stream Carriers. In this product those Carriers are TCP connections that may traverse opaque Relay nodes.

The protocol endpoints are:

- **Client** — MPTCP Desk or Linux Client;
- **Landing** — `mptcp-landing`.

Relay is not an MPX endpoint and does not need the Transport Key.

## 2. Session, Carrier and Stream

A **Session** is the authenticated logical transport between Client and Landing.

A **Carrier** is one authenticated transport path attached to the Session. Carriers may arrive, disappear and be replaced while the Session and Streams continue.

A **Stream** is an ordered reliable application byte stream multiplexed inside the Session. Multiple Streams may be active concurrently and may use multiple Carriers concurrently.

The protocol allows Carrier IDs in the MPX VarInt space. The current product implementation limits simultaneously active Carriers to **8**.

## 3. Stable handshake and authentication

The stable handshake authenticates both endpoints using the pre-shared Transport Key and binds negotiated parameters to the handshake transcript. The implementation uses the protocol-defined SHA-256/HMAC/HKDF construction and AES-256-GCM protected records.

Operational requirements:

- Client and Landing must use the same Transport Key;
- Transport Key must never be placed on an opaque Relay;
- a Carrier that fails authentication is not admitted to a Session;
- JOIN must match the retained Session identity and negotiated limits.

The implementation applies a short pre-handshake admission deadline and a bounded authenticated handshake timeout to prevent silent unauthenticated connections from holding server resources indefinitely.

## 4. Secure records and frames

After authentication, MPX Frames are carried inside authenticated encrypted records. Important frame classes include:

- Stream open / open result;
- DATA;
- ACK;
- Stream WINDOW;
- Session WINDOW;
- FIN / RST / reset / stop-receiving;
- liveness / control frames;
- transmission retirement;
- Carrier and Session close.

The maximum STREAM_DATA payload in v1.1.1 is **32 KiB** and the maximum record size is **64 KiB**.

## 5. Reliability and cross-Carrier reinjection

Reliable DATA is tracked with Session-wide transmission identity rather than assuming one TCP Carrier is permanently responsible for one application byte range.

This permits:

- DATA for one Stream to travel over different Carriers;
- cross-Carrier out-of-order arrival;
- retransmission or reinjection after a Carrier degrades or disappears;
- duplicate/replay detection;
- retirement only after the protocol state proves the transmission no longer needs to remain recoverable.

A Carrier failure does not automatically destroy the Streams that used it.

## 6. Flow control

MPX/4 has both Stream-level and Session-level receiver credit.

v1.1.1 implementation limits:

```text
Max Streams:                 2048
Max STREAM_DATA:             32 KiB
Max per-Stream receive win:  16 MiB
Session receive-credit:      128 MiB
Physical receive accounting: 128 MiB
```

### v1.1.1 send-credit rule

The peer's advertised MPX windows are authoritative:

```text
Stream send room  = peer_stream_limit - stream_tx_next
Session send room = peer_session_limit - session_tx_committed
```

A DATA send requires room in both protocol windows and room in local resource queues.

v1.1.1 deliberately does **not** use the old local `txUsed` / `txGrowth` ledger as a second protocol-like admission pool. Those fields can remain in telemetry as diagnostic mirrors.

Local protection still exists for:

- pending frame count;
- pending DATA bytes (1 GiB pool);
- bootstrap pending-capacity fairness;
- Carrier flight/budget;
- receiver memory/page accounting;
- lifecycle limits.

These local resources must not be confused with peer-advertised MPX flow-control credit.

## 7. Concurrency model

MPX/4 requires some Session-wide shared state, including Session WINDOW, transmission identity, Carrier lifecycle and replay/retirement state. The protocol therefore does not imply that every Stream can be implemented with zero shared synchronization.

v1.1.1 reduces contention by using:

- per-Stream ready queues/rings;
- bounded dispatcher batches;
- Stream-local receive synchronization;
- short Session critical sections;
- per-Stream targeted writer wakeups;
- receive payload work outside long Session locks where safe.

The concurrency implementation changes performance characteristics without changing wire semantics.

## 8. Scheduler relationship

MPX/4 Stable Core does not negotiate the product's Auto/Aggregate/Protect/Weighted mode as a wire Scheduler ID. Scheduler mode is endpoint-local policy.

The Client scheduler chooses Carriers for Client-to-Landing DATA. The Landing scheduler chooses Carriers for Landing-to-Client DATA. They may differ, although matched modes are usually easier to operate.

A scheduling decision cannot override protocol eligibility, flow-control limits, transmission identity or replay rules.

## 9. Capacity hints

The product can use the published MPX/4 receive-capacity hint extension to communicate directional capacity information used by local scheduling. Weighted mode also consumes configured direction capacities.

A capacity value is a scheduling prior. It is not a reservation, fixed percentage or guaranteed throughput.

## 10. UDP is outside MPX/4 Core

Native UDP uses the separate authenticated MPU/1 data plane.

UoT is a product service that carries UDP payload through an authenticated TCP Carrier Session. It reuses MPX transport but does not redefine MPX/4 Core as a datagram protocol.

## 11. Product fast-start

The product layer can keep a bounded pool of authenticated, already-opened Streams for TCP/UoT service startup. This removes avoidable service-setup RTTs from the hot path without changing MPX/4 Core frame grammar.

## 12. Resource and implementation limits

Current product constants include:

```text
Active Carriers:       8
Streams:               2048
Pending frames:        32768
Pending DATA bytes:    1 GiB
Receive buffer account:128 MiB
```

These are implementation/resource limits, not all protocol namespace limits.

## 13. Linux TCP congestion control is below MPX/4

MPX/4 does not specify Linux TCP congestion-control algorithms. For the current deployment profile the project recommends:

- **Landing: CUBIC**;
- **Relay: BBR**, preferably with `fq`.

This changes the behavior of underlying TCP Carriers, not MPX/4 wire compatibility. See [Network tuning](../guides/NETWORK-TUNING.md).
