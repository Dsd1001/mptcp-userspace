# MPTCP Desk 1.0.0 validation

1.0.0 Mac release candidate must preserve every 0.10.x product capability while moving the Userspace engine to MPX/4 Protocol Version 4 Stable.

Required checks include:

- engine/Landing Go test, vet and race coverage;
- the exact 20 Stable Core vectors from `protocol-v4.0.0`;
- MAX_CARRIERS, Stable CREATE/JOIN, DORMANT, TRANSMISSION_RETIRE, confirmation/replay, credit reordering and error-scope tests;
- Auto / Aggregate / Protect / Weighted local-policy regression;
- Provisioning test/vet and encrypted Profile/Bundle/device behavior;
- persistent LKG and cache-first recovery;
- parallel Profile fault isolation and exact 1s/2s/5s/10s/30s/30s retry schedule;
- remote management default-off/local-only enable and signed-update whitelist;
- arm64 and x86_64 Swift typecheck and UI smoke;
- Universal DMG plus Linux Client/Landing/Provisioning amd64+arm64 builds;
- one Source-ID and version 1.0.0 across the suite;
- source-matched Scheduler, capacity and 180s runtime evidence.

## Frozen Broker continuity

The main App must contain no direct SecItem access for Transport Key, Provisioning URL or Remote Control credential.

`MPTCPKeychainBroker.v1.b64` must decode to the exact 0.10.12 Broker v1 binary:

- SHA-256 `5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`;
- Universal arm64+x86_64;
- identifier `org.mptcp.desktop.keychainbroker.v1`;
- unchanged pinned parent Designated Requirement.

The build script fails before App construction if Broker bytes differ. The release verifier independently checks resource equality, decoded hash, signature, architecture and requirement.

A final update-continuity acceptance must upgrade an installed 0.10.12 App to 1.0.0 through Sparkle and confirm the parent App changes while the Broker resource remains byte-identical and Keychain access does not return to repeated authorization prompts.

Correctness tests do not constitute WAN performance validation. CAPACITY.json and RUNTIME.json remain separate source-matched evidence.

The macOS DMG uses the pinned stable local self-signed identity and is not Apple Developer ID notarized.
