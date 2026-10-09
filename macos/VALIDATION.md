# MPTCP Desk v1.1.2 validation

This file summarizes the Mac-specific validation boundary for the current release.

## Identity

```text
MPTCP Desk:         1.1.2
MPX wire version:   4
Protocol release:   protocol-v4.0.0
Protocol source:    44f587fd279ed2238b070dd68114c76822353f4d
Capability revision:8
```

## Required Mac checks

- Swift typecheck/build for arm64 and x86_64;
- Universal App/DMG architecture verification;
- embedded engine version and Source-ID match the suite source;
- local/managed Profile and Bundle parsing;
- parallel Profile isolation/recovery;
- UI smoke for current scheduler/diagnostic surfaces;
- Sparkle appcast/EdDSA verification;
- no direct main-App Keychain access for the Broker-owned secrets;
- frozen Broker resource verification.

## Frozen Broker continuity

`MPTCPKeychainBroker.v1.b64` must decode to:

```text
SHA256 5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9
```

The Broker remains Universal arm64+x86_64 and keeps its pinned v1 identity/parent requirement. The main App may change between releases; the frozen v1 Broker bytes must not silently change with it.

## Engine correctness inherited by Desk

MPTCP Desk uses the same userspace engine that is validated for:

- MPX/4 Stable handshake and records;
- Stream/Session WINDOW flow control;
- v1.1.1 peer-WINDOW-authoritative send-credit semantics;
- concurrent multi-Stream/multi-Carrier operation;
- retransmission/reinjection;
- Auto/Aggregate/Protect/Weighted;
- UoT product path.

## Performance boundary

Mac correctness/UI/build validation is not a physical-WAN throughput claim. v1.1.2 adds local Queue-aware Admission; the shaped-loopback A/B does not establish WAN improvement. Native MPTCP transport and its UI/sysctl operation are removed.

For production path comparisons, document the Linux host baseline separately:

```text
Landing = CUBIC
Relay   = BBR (fq preferred)
```

These are deployment settings below the Mac/MPX product layer, not Mac build conditions.

The current release uses the pinned stable local self-signed code-signing identity by default unless an explicit Developer-ID build is requested.
