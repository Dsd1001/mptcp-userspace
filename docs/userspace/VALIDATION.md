# v0.10.12 / MPX/4 Draft 04 validation and release limits

This document describes the current release gate. Historical version-specific validation files are not the source of truth for v0.10.12.

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
18. Parallel Profile supervisor retry schedule exactly 1s/2s/5s/10s/30s then 30s indefinitely.
19. One failed/recovered parallel Profile must not restart a healthy peer.
20. All-down parallel Bundle must remain alive and recover when child runtimes become available.
21. Runtime crash reconnect, cancellation and permanent-failure no-busy-loop regressions.
22. Universal macOS DMG plus Linux amd64/arm64 Client, Landing and Provisioning builds.
23. Frozen-source reproducibility and Source-ID binding for published artifacts.
24. Frozen Keychain Broker gate: pinned Base64 decodes to the expected signed Universal binary; direct untrusted-parent invocation is rejected; two differently built parent Apps with the same pinned Designated Requirement can access the same validation item through one unchanged Broker cdhash.

## Current implementation bounds

Release validation assumes the v0.10.12 current bounds:

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

## 0.10.12 control/update gates

- Device pairing secret isolation, revocation, observed-state report and offline desired-state persistence.
- Exact whitelist of start/stop/config-sync/restart/signed-update behavior; unknown command fields rejected.
- Mac local-only policy: remote-management enable/server URL cannot be provisioned remotely.
- Swift arm64/x86_64 typecheck and Settings UI smoke with remote management default-off.
- Sparkle 2.10.0 dependency SHA-256 pin, embedded public key/feed URL, appcast XML validation and EdDSA verification.
- The main App contains no direct SecItem access for the Transport Key, Provisioning URL or Remote Control credential.
- `MPTCPKeychainBroker` v1 is byte-for-byte pinned at SHA-256 `5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`, Universal arm64+x86_64, and signed with identifier `org.mptcp.desktop.keychainbroker.v1`.
- Broker parent authentication requires `org.mptcp.desktop` plus the pinned local certificate root; a shell/unsigned parent is rejected.
- The Broker validation item proves Keychain `partition_id` remains bound to the unchanged Broker cdhash across two parent App cdhash generations.
- The v0.10.12 Broker resource must be byte-identical to v0.10.11; rebuilding or re-signing Broker v1 is a release failure.
- The newly signed 0.10.12 App must have a different cdhash from installed 0.10.11 while retaining the same pinned Designated Requirement.

## Production boundary

Packaging or documentation work must not silently modify production Landing, Provisioning, Relay, firewall, backend, Surge or Native MPTCP services.

A production upgrade is a separate operation with backups, hash verification, service health checks and rollback readiness.

The release package TESTS.json, PROVENANCE.json, SCHEDULER-MODES.json, CAPACITY.json, RUNTIME.json and ACCEPTANCE.md are the machine-readable evidence records.
