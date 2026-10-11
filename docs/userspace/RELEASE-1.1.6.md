# MPTCP Userspace v1.1.6 — Weighted A+B Interim Release

Released October 11, 2026 at the user's request before the complete WO-01 performance acceptance.
This is a source-matched suite implementation release, **not a claim that 150-Stream performance has been fixed**.

A1 implements work-conserving Weighted DATA carrier selection (except for a latched high-fan-in safety fallback) with Socket writer stall awareness. A2 bounds adaptive flight using minimum RTT, observed delivery, a cooldown and a Session-wide cap. B1 provides a bounded first-DATA fast lane with shared Session tokens and Bulk fairness, preserving MPX/4 Stable wire compatibility and the v1.1.5 control frame bypass.

### Preliminary shaped-loopback evidence
On six 92 Mbps paths (5/15/25/40/65/100 ms one-way), 12 x 64 MiB five-round median improved from roughly 391 to 543 Mbps; short 32 KiB completion P95 was roughly 186 vs 179 ms in the selected earlier A+B experiment. These are **not** WAN results or a complete re-validation of the final release revision.

**Known limitation:** synchronous 150 x 4 MiB runs remain unstable. An earlier five-round batch showed 283 Mbps Legacy median vs 225 Mbps A+B median, with multi-second socket stalls. A+B cannot pass the 150-stream performance gate; no claim of resolution. Full acceptance, physical deployment and regression investigations remain outstanding.

All components use 1.1.6. macOS's pinned v1 Keychain Broker and stable local signing identity remain unchanged; the local self-signed identity is not notarization. Auto/Aggregate/Protect scheduler semantics and MPX/4 Protocol Version 4 Stable are unchanged. No production rollout is performed. Keep v1.1.5 assets for rollback.
