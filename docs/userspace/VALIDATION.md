# v1.0.0 / MPX/4 Protocol Version 4 Stable validation and release limits

This document is the current v1.0.0 release gate. Historical release documents are not the source of truth for 1.0.0.

## Required protocol and correctness gates

A Stable release candidate must pass, at minimum:

1. Engine/Landing full Go test and vet suites, plus race-enabled multipath/runtime coverage.
2. The exact 20 frozen Core JSON vectors from MPX/4 `protocol-v4.0.0` / commit `44f587fd279ed2238b070dd68114c76822353f4d`.
3. Stable key schedule, Finished, AES-256-GCM Secure Record and canonical VarInt/Frame checks.
4. MAX_CARRIERS, full VarInt Carrier identity, Generation replacement and active-count admission.
5. VERSION_NEGOTIATION and classified HANDSHAKE_REJECT behavior.
6. DORMANT retention/recovery and no new OPEN/DATA commitment while no Carrier is usable.
7. Transmission allocation/confirmation validity, replay retention and TRANSMISSION_RETIRE.
8. Reordered Stream/Session credit handling, final-size and terminal-flow-control rules.
9. Stable error-scope and superseded-Carrier receive/output isolation.
10. Published RECEIVE_CAPACITY_HINT extension encoding/invalid cases.
11. Auto / Aggregate / Protect / Weighted regressions as endpoint-local policies.
12. Provisioning full test/vet, including Profile/Bundle validation, encrypted envelope and device control.
13. LKG cache, cache-first launch, refresh/retry and parallel Bundle isolation/recovery.
14. Exact Profile reconnect schedule 1s/2s/5s/10s/30s then every 30s.
15. Swift arm64/x86_64 typecheck and macOS UI smoke.
16. Linux amd64/arm64 Client, Landing and Provisioning builds/runtime smoke.
17. Universal macOS DMG, Sparkle appcast/EdDSA verification and frozen-source reproducibility.
18. One Source-ID and one **1.0.0** component version across Desk/Linux Client/Landing/Provisioning.

## Scheduler/capacity/runtime evidence

Scheduler correctness and performance are separate from Core protocol conformance. The v1 scheduler record uses local **scheduler policy revision 6** and must not claim an MPX/4 wire Scheduler ID.

A formal 1.0.0 release requires fresh, source-matched:

- `SCHEDULER-MODES.json`;
- ten 30-second capacity cases: 128/256/512/1024/2048 simultaneous Streams × two rounds;
- the independent 180-second mixed runtime scenario;
- `TESTS.json` and `PROVENANCE.json`.

The old absolute 300/500 Mbps uniform-path thresholds are not used as a release gate because they vary with the validation host's loopback scheduler and CPU conditions. The historical Draft-era rule that required the heterogeneous `300+20+180 Mbps` result to reach 95% of the fastest-only path is also not used for Stable 1.0 because unchanged 0.10.12 `origin/main` does not satisfy that rule. Instead, Auto and Aggregate uniform matrices and Auto/Aggregate/Protect heterogeneous measurements are repeated on the same host for both the frozen 0.10.12 commit `e5f6a33868031dd33c0557942ed2ecc4e2d75998` and the 1.0.0 candidate; each candidate per-case median must retain at least 90% of the 0.10.12 median. Raw baseline/candidate evidence, repetition counts and hashes are part of the scheduler record.

Passing unit tests alone does not prove WAN throughput or production behavior.

## Current implementation bounds

- 2–8 configured Relays per Profile;
- local/effective active Carrier limit up to 8 for this product;
- CARRIER_ID wire space: non-zero MPX VarInt, 1 through 2^62-1;
- 2048 active peer-initiated Streams;
- 32 KiB STREAM_DATA maximum;
- 32 KiB cold per-Stream receive-window floor and bootstrap-accounting unit;
- up to 192 KiB initial/standby receive-window target under RC4 pressure scaling;
- 16 MiB per-Stream receive-credit maximum;
- 128 MiB Session receive-credit, split into a 64 MiB maximum bootstrap pool and 64 MiB growth pool at the 2048-Stream bound;
- 16 MiB RC4 connection-storm initial-window share budget and 64 MiB established-standby share budget; these are pressure-scaling numerators, not separately reserved credit pools. WINDOW entitlement itself does not reserve committed receive credit;
- 128 MiB physical receive-page accounting.

RC4 preserves MPX/4 wire semantics and the existing hard memory limits while refining the local receive-credit controller. A fresh Weighted Stream uses `clamp(16 MiB / live Streams, 32 KiB, 192 KiB)` as its initial target; this gives the current ~53-Stream production workload 192 KiB per Stream while reducing 128/256/512+ connection storms to 128/64/32 KiB. Established idle Streams use the larger `clamp(64 MiB / live Streams, 32 KiB, 192 KiB)` standby target. A single fresh non-Weighted Stream retains the RC3 32 KiB cold-start path so Auto/Aggregate/Protect single-stream performance is not regressed.

Active Weighted sessions, and non-Weighted sessions with real multi-Stream demand, continue to use continuous BDP-aware autotuning driven by measured application consumption and capacity-weighted base RTT. WINDOW is refreshed at roughly 50% remaining credit or earlier when predicted exhaustion would precede the next feedback opportunity. Each Stream also keeps an endpoint-local warm high-water history: after idle, the active target may fall back to standby without revoking already advertised credit, while the proven warm target decays with a 30-second half-life and expires after two minutes. A subsequent real burst that consumes one 32 KiB cold unit can rapidly restore that Stream's warm target, bounded by current Session fair-share.

The RC3 DATA-receipt timeout hysteresis is retained: one timeout retransmits without removing a healthy Carrier from scheduling. A Carrier is temporarily deprioritized only after a second timeout epoch without intervening DATA progress; successful DATA progress clears the timeout suspicion. This is endpoint-local behavior and does not change MPX/4 wire semantics.

## Keychain Broker / updater continuity

The 1.0.0 Mac App preserves the 0.10.12 Broker split.

- Main App has no direct SecItem access for Transport Key, Provisioning URL or Remote Control credential.
- `MPTCPKeychainBroker` remains **v1**.
- Base64 must decode to SHA-256 `5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`.
- Broker identifier/parent requirement remains unchanged.
- Rebuilding, re-signing or replacing Broker v1 is a release failure.
- A 0.10.12 → 1.0.0 Sparkle update must install a changed parent App while reusing the exact same Broker bytes, without recreating the repeated Keychain authorization problem.
- Remote-management enable/server URL remains local-only; remote control cannot enable itself or run arbitrary commands.

## Production boundary

Packaging does not silently change production Landing, Provisioning, Relay, firewall, backend, Surge or Native MPTCP services. Deployment is a separate operation with backup, hash verification, service health checks and rollback readiness.
