# Validation — MPTCP Userspace v1.1.1

This document defines how to interpret validation for the current v1.1.1 suite.

## Release identity

The current source declares:

```text
Version:             1.1.1
Wire protocol:       4
Capability revision: 8
Protocol release:    protocol-v4.0.0
Protocol source:     44f587fd279ed2238b070dd68114c76822353f4d
```

Validation records must match the source identity they claim to validate.

## Correctness gates

The v1.1.1 release regression scope includes, at minimum:

- engine package tests;
- Landing/multipath tests;
- Provisioning tests;
- `go vet` for engine and Provisioning;
- `go test -race ./multipath`;
- MPX/4 Stable handshake/record/frame vectors and error scopes;
- CREATE/JOIN, Carrier lifecycle and cross-Carrier reliability cases;
- Stream/Session flow-control cases;
- peer-WINDOW-authoritative v1.1.1 send-credit regression;
- pending frame/byte resource protection;
- concurrent cross-Carrier Stream receive/publication cases;
- Auto/Aggregate/Protect/Weighted regressions;
- Bundle/Profile isolation and reconnect supervision;
- UoT/product fast-start cases;
- Linux amd64/arm64 build verification;
- macOS Universal build, signing/update and frozen Broker verification.

## v1.1.1 flow-control acceptance

A conforming v1.1.1 implementation must preserve these distinctions:

- peer Stream WINDOW is a real protocol send gate;
- peer Session WINDOW is a real protocol send gate;
- local pending/flight/memory bounds remain hard resource gates;
- legacy `txUsed` / `txGrowth` mirrors do **not** reintroduce the old second send-admission pool;
- Session receive-credit hard limit remains 128 MiB.

## Concurrency acceptance

The v1.1.0+ data-plane refactor must continue to allow useful concurrency across Streams and Carriers while preserving shared Session semantics. Tests cover bounded dispatcher work, Stream-local receive work, cross-Carrier publication ordering and targeted wakeups.

Correctness requires retaining shared Session state where the protocol requires it; it does not mean making all protocol accounting thread-local.

## Product/control-plane acceptance

Current validation also covers:

- Profile/Bundle schema validation;
- encrypted Provisioning envelope;
- Last Known Good cache behavior;
- independent parallel Profile supervision;
- reconnect schedule `1s -> 2s -> 5s -> 10s -> 30s -> every 30s`;
- remote management default-off and local-only enable/server configuration;
- absence of arbitrary remote shell execution;
- frozen Keychain Broker continuity.

## Frozen Broker

The `MPTCPKeychainBroker` v1 resource remains pinned to:

```text
SHA256 5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9
```

A main-App update must not silently rebuild/re-sign/replace the frozen v1 Broker under the same identity.

## Performance and WAN boundary

Correctness tests do not prove physical-WAN throughput. Scheduler/capacity/runtime evidence is separate from protocol correctness and must be tied to the exact source and test environment.

v1.1.1 specifically did **not** run a new WAN/capacity/high-BDP performance promotion before release. Do not infer a new throughput guarantee from the correctness result.

## Deployment validation

Before collecting performance evidence on Linux hosts, establish and record the recommended baseline:

```text
Landing congestion control: CUBIC
Relay congestion control:   BBR
Relay qdisc:                 fq preferred
```

At minimum capture:

```sh
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
ss -s
ss -ti
```

Then correlate with product telemetry: Carrier RTT/goodput/queue/outstanding, retransmission/reinjection, Stream/Session WINDOW waits and CPU.

This host baseline is not a wire-protocol conformance condition, but it is the recommended reference configuration for production performance comparisons.
