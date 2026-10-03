# v0.10.3 / MPX/4 Draft 04 validation and release limits

This document describes the current release gate. Historical version-specific validation files are not the source of truth for v0.10.3.

## Required correctness gates

A release candidate must pass, at minimum:

1. Go engine/Landing full test suite and go vet.
2. Race-enabled multipath/runtime tests.
3. MPX/4 VarInt, Frame, key-schedule, Finished and Secure-Record vectors.
4. Draft 04 Carrier Generation and Error Scope semantic vectors.
5. Scheduler regressions for Auto / Aggregate / Protect / Weighted.
6. Provisioning full test/vet, including Profile/Bundle validation and encrypted response envelope.
7. Wrong-secret/tamper rejection and fresh nonce behavior for Provisioning encryption.
8. Legacy plaintext managed-response compatibility.
9. Bundle single_select/parallel selection and local-port conflict tests.
10. Parallel runtime fault isolation: one failed Profile must not cancel healthy Profile runtimes.
11. Swift arm64 and x86_64 typecheck.
12. macOS offscreen UI rendering for local/remote configuration and path diagnostics.
13. Remote Bundle per-Profile telemetry isolation and hidden Relay endpoint rendering.
14. Default-collapsed and expanded Stream/Lifecycle and Window/Credit resource panels.
15. Linux amd64 native runtime validation of managed encrypted Profile/Bundle configuration.
16. Universal macOS DMG plus Linux amd64/arm64 Client, Landing and Provisioning builds.
17. Frozen-source reproducibility and Source-ID binding for published artifacts.

## Current implementation bounds

Release validation assumes the v0.10.3 current bounds:

- 2–8 configured Relays per Profile;
- up to 8 MPX/4 Carriers per Session;
- 2048 active peer-initiated Streams;
- 32 KiB STREAM_DATA maximum;
- 16 MiB per-Stream receive-credit maximum;
- 128 MiB Session receive-credit;
- 128 MiB physical receive-page accounting.

Changing any of these is a separate capability/scale change and requires dedicated validation rather than documentation-only edits.

## Evidence separation

Correctness, laboratory performance, capacity, build provenance and production/App+Surge validation are separate evidence classes.

A passing unit/race/offscreen test suite does not by itself prove WAN throughput or a specific production deployment. Performance claims must be tied to source-matched CAPACITY/RUNTIME evidence.

## Production boundary

Packaging or documentation work must not silently modify production Landing, Provisioning, Relay, firewall, backend, Surge or Native MPTCP services.

A production upgrade is a separate operation with backups, hash verification, service health checks and rollback readiness.

The release package TESTS.json, PROVENANCE.json, SCHEDULER-MODES.json, CAPACITY.json, RUNTIME.json and ACCEPTANCE.md are the machine-readable evidence records.
