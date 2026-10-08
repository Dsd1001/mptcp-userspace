# Delivery and release artifacts — v1.1.1

MPTCP Userspace releases are delivered as a version-matched suite rather than unrelated component binaries.

## Current release identity

```text
Version:          1.1.1
Wire protocol:    4
Protocol release: protocol-v4.0.0
Protocol source:  44f587fd279ed2238b070dd68114c76822353f4d
```

A release should have one source identity across MPTCP Desk, Linux Client, Landing and suite-mode Provisioning.

## v1.1.1 assets

The published v1.1.1 release includes:

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
appcast.xml
PROVENANCE.json
TESTS.json
RUNTIME.json
CAPACITY.json
SCHEDULER-MODES.json
ACCEPTANCE.md
RELEASE-NOTES-v1.1.1.md
```

## What evidence means

- `PROVENANCE.json` ties artifacts to the frozen source identity.
- `TESTS.json` records correctness/build validation.
- `SCHEDULER-MODES.json` records scheduler-specific evidence.
- `RUNTIME.json` / `CAPACITY.json` hold runtime/performance evidence generated for their corresponding validation scope.
- `ACCEPTANCE.md` summarizes the release acceptance boundary.

Evidence is source-specific. Do not carry an old JSON evidence file forward after changing source and call it current.

## v1.1.1 performance claim boundary

v1.1.1 was promoted after correctness/concurrency/build regression. It did **not** claim a new physical-WAN throughput or capacity benchmark.

Therefore deployment documentation must not turn historical lab numbers into a current WAN guarantee. Production operators should establish a real-path baseline and record loaded RTT, goodput, queue/outstanding, retransmission and CPU as well as throughput.

## Production delivery baseline

Before judging a production deployment, establish the recommended host roles:

- **Landing = CUBIC**;
- **Relay = BBR**, preferably with `fq`.

These are deployment settings and are not embedded into the release binaries. Native UDP is unaffected by TCP congestion control.

See [Network tuning](../guides/NETWORK-TUNING.md).

## Upgrade safety

A production delivery should include:

1. artifact hash verification;
2. previous binary/config backup;
3. version and Source-ID check after restart;
4. real Client authentication and backend reachability;
5. Landing/Relay congestion-control verification;
6. rollback readiness.

Packaging alone must not silently rewrite unrelated services, firewall rules, routing or other tunnels.
