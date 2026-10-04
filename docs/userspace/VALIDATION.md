# v0.10.4 / MPX/4 Draft 04 validation and release limits

This document describes the current release gate. Historical version-specific validation files are not the source of truth for v0.10.4.

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
9. Persistent managed LKG cache round-trip, URL-fingerprint isolation, 0600 file mode, selected-ID persistence and cache-first launch planning.
10. 48-hour refresh due/retry policy and proof that a successful refresh is not applied immediately while a runtime is active.
11. Bundle single_select/parallel selection and local-port conflict tests.
12. Parallel runtime fault isolation: one failed Profile must not cancel healthy Profile runtimes.
13. Swift arm64 and x86_64 typecheck.
14. macOS offscreen UI rendering for local/remote configuration and path diagnostics.
15. Remote Bundle per-Profile telemetry isolation and hidden Relay endpoint rendering.
16. Default-collapsed and expanded Stream/Lifecycle and Window/Credit resource panels.
17. Linux amd64 native runtime validation of managed encrypted Profile/Bundle configuration.
18. Universal macOS DMG plus Linux amd64/arm64 Client, Landing and Provisioning builds.
19. Frozen-source reproducibility and Source-ID binding for published artifacts.

## Current implementation bounds

Release validation assumes the v0.10.4 current bounds:

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
