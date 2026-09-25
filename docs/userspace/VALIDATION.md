# 0.9.4 / MPX/3 Rev5 validation and release limits

0.9.4 is the formal Weighted feature release. Release evidence must bind the final frozen Source-ID. Unit success is not a throughput claim, and an offscreen render is not physical App+Surge acceptance.

The required 0.9.4 feature gates are:

1. Go full test and vet for the complete engine and Landing packages.
2. Race-enabled tests covering the multipath package and Weighted protocol/direction/fault paths.
3. Swift type-check plus the existing UI/Profile harnesses for both old profiles and Weighted fields.
4. Authenticated Weighted hello tests proving download/upload fields are inside the HMAC transcript and legacy 0x41/0x42/0x43 hello bytes remain unchanged.
5. Direction tests proving Landing→Mac uses required download capacity and Mac→Landing uses optional upload capacity; omitted upload must fall back to learned Aggregate behavior.
6. Fault tests proving configured capacity does not override disconnect, penalty or delivery-timeout/reinjection protection.
7. Source-matched laboratory high-BDP Weighted runs using six 50 Mbps paths and the observed RTT set used during development. The report must retain all repetitions and not discard a slow sample.

The 2048 stream, 128 MiB session credit, 32/96 MiB bootstrap/growth split, 128 MiB sender DATA pending, 128 MiB physical receive allocation and 16 MiB per-stream window limits are unchanged. Existing capacity boundary tests remain valid regression targets and must not be weakened to make Weighted pass.

The formal Weighted release package may be produced when current-source correctness and Weighted laboratory gates pass even if the full ten-case 30-second capacity matrix and physical 180-second App+Surge run have not been rerun. In that case CAPACITY.json and RUNTIME.json must explicitly say `not-run-for-weighted-release`; ACCEPTANCE.md must not imply those higher-level gates were completed.

A production deployment is separate from package creation. HKT, installed App, Surge, Soga, Relay, firewall and Native services are not modified by the validation or packaging scripts.

The delivery's ACCEPTANCE.md, TESTS.json, SCHEDULER-MODES.json, PROVENANCE.json, CAPACITY.json and RUNTIME.json are the source of truth. Laboratory tests do not certify a user's provider bandwidth, multi-day stability, physical Intel hardware or security.
