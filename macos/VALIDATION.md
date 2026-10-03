# MPTCP Desk 0.10.3 validation

A 0.10.3 release candidate must cover:

- engine/Landing go test, go vet and race suite;
- MPX/4 Draft 04 protocol vectors and error/generation semantics;
- Auto / Aggregate / Protect / Weighted regressions;
- Provisioning Profile/Bundle CRUD and encrypted envelope behavior;
- managed URL HTTPS/loopback rules, redirect rejection and response-size limits;
- single_select and parallel Bundle validation;
- atomic duplicate/occupied local-port preflight;
- one-bad/one-good parallel runtime isolation;
- per-Profile remote diagnostic state isolation;
- hidden Relay endpoint rendering for managed diagnostics;
- Stream/Lifecycle and Window/Credit disclosure panels default-collapsed and expanded;
- arm64 and x86_64 Swift typecheck;
- local/remote home-page offscreen rendering;
- Universal DMG build;
- Linux Client/Landing/Provisioning amd64+arm64 builds;
- frozen-source rebuild/provenance checks;
- one Source-ID across the published suite.

Correctness tests do not constitute WAN performance validation. Source-matched CAPACITY.json and RUNTIME.json remain separate evidence.

The macOS DMG is ad-hoc signed and not notarized.
