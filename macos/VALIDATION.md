# MPTCP Desk 0.10.12 validation

A 0.10.12 release candidate must cover:

- remote management is default-off and desired control is ignored while locally disabled;
- HTTPS-only control-server validation, with loopback HTTP allowed only for development;
- per-device pairing/auth isolation and Keychain-only device secret handling;
- signed Sparkle appcast / DMG metadata, pinned public key and embedded framework;
- no direct main-App SecItem access for the three MPTCP Desk secrets;
- frozen Keychain Broker v1 resource hash, Universal architectures, code signature and parent Designated Requirement enforcement;
- the Broker v1 Base64 resource is byte-identical to the v0.10.11 tag and still decodes to SHA-256 `5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9` / cdhash `b74dfc856d47e78d221e6f08bd1fe8c359ef8f96`;
- the 0.10.12 App cdhash differs from the installed 0.10.11 App while the App Designated Requirement remains identical;
- two changed parent-App cdhash values successfully use the same Broker-backed validation Keychain partition;
- untrusted direct Broker invocation is rejected;
- remote update cannot specify an arbitrary URL or executable;

- engine/Landing go test, go vet and race suite;
- MPX/4 Draft 04 protocol vectors and error/generation semantics;
- Auto / Aggregate / Protect / Weighted regressions;
- Provisioning Profile/Bundle CRUD and encrypted envelope behavior;
- managed URL HTTPS/loopback rules, redirect rejection and response-size limits;
- persistent LKG cache round-trip, URL-fingerprint isolation, 0600 permissions and selected Profile ID persistence;
- cache-first launch planning, 48-hour refresh/retry policy and no immediate apply/restart while a runtime is active;
- single_select and parallel Bundle validation;
- atomic duplicate/occupied local-port preflight;
- one-bad/one-good parallel runtime isolation and automatic recovery without restarting the healthy peer;
- exact 1s/2s/5s/10s/30s/30s Profile retry schedule;
- all-down parallel Bundle supervisor survival/recovery;
- runtime crash reconnect, cancellation and permanent-failure backoff;
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

The macOS DMG uses the pinned stable local self-signed identity and is not Apple Developer ID notarized. The frozen Keychain Broker is independently pinned by exact SHA-256 and must not be rebuilt or re-signed while remaining v1.
