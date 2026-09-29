# 0.9.5 / MPX/3 Rev5 validation and release limits

0.9.5 is the background-resident and sleep/wake recovery release on top of the 0.9.4 Weighted transport. The wire protocol remains capability revision 5. Release evidence must bind the final frozen Source-ID.

Required 0.9.5 feature gates:

1. Pure Swift lifecycle-policy harness proving recovery is allowed only when background resident and the persisted run intent are both enabled, and is suppressed during sleep, quit and explicit manual stop.
2. Bounded restart schedule verification for 1 / 2 / 5 / 10 / 30 seconds with a 30-second cap.
3. macOS 13 compile/type-check for ServiceManagement, NSWorkspace sleep/wake and Network path monitoring.
4. Existing Profile/UI/Scheduler offscreen harnesses, now compiled with Lifecycle.swift.
5. Go full test and vet for the engine/Landing packages.
6. Race-enabled multipath and Weighted/main tests.
7. Actual stdin engine coverage for Auto / Aggregate / Protect / Weighted.
8. Rev5/Weighted direction, optional-upload fallback, penalty/timeout/reinjection and source-matched six-path high-BDP regression.
9. Universal DMG and Linux Landing provenance verification rebuilt from the frozen source archive.

The 0.9.5 lifecycle change must not modify MPX/3 hello bytes, capability revision, frame formats, 2048-stream bound, 128 MiB session credit, 128 MiB sender DATA pending, 128 MiB physical receive allocation or 16 MiB per-stream maximum window.

The lifecycle harness is deterministic logic coverage. It does not prove that a particular user's macOS login-item privacy setting grants launch-at-login. The real App reports SMAppService status, including requires-approval. Offscreen UI rendering also does not simulate a physical lid-close, Wi-Fi reassociation or actual login session.

The formal background-resident release package may be produced when current-source lifecycle/correctness gates and the unchanged Weighted laboratory gates pass, even if the full ten-case 30-second capacity matrix and physical 180-second App+Surge run are not rerun. In that case CAPACITY.json and RUNTIME.json must explicitly say `not-run-for-background-release`.

A production deployment is separate from package creation. Validation and packaging must not replace the installed App, deploy HKT, or modify Surge, Soga, Relay, firewall or Native services.

The delivery's ACCEPTANCE.md, TESTS.json, SCHEDULER-MODES.json, PROVENANCE.json, CAPACITY.json and RUNTIME.json remain the source of truth.
