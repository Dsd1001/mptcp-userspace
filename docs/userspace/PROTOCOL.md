# MPX/3 capability revision 5 (0.9.4)

MPX is a custom application transport over ordinary TCP carriers, not kernel MPTCP and not QUIC. Each carrier explicitly disables native MultipathTCP. Backend bytes remain opaque. Native fallback and UDP datagram transport remain separate.

## Authentication, scheduler and Weighted capacity

The 48-byte authenticated hello retains MPX3 family framing and binds carrier/session identity, random challenge, scheduler policy and the fixed-size capacity fields into the existing HMAC transcript.

Byte 7 is:

- `0x41` Auto
- `0x42` Aggregate
- `0x43` Protect
- `0x44` Weighted

For Auto/Aggregate/Protect, bytes 40..43 remain zero exactly as in 0.9.3. For Weighted, bytes 40..41 are the configured download capacity and 42..43 are the configured upload capacity, each a big-endian uint16 in 0.1 Mbps units. Download must be non-zero. Upload zero means that the client omitted the optional upload capacity and the Mac sending direction must use normal learned Aggregate capacity instead.

The supported configured range is 0.1–6553.5 Mbps with 0.1 Mbps precision. The Landing sending direction uses download capacity; the Mac sending direction uses upload capacity when supplied. Capacity values are authenticated before the Landing allocates or joins the session, so a network intermediary cannot silently change weights.

0.9.4 Auto/Aggregate/Protect keep the 0.9.3 0x41/0x42/0x43 hello values and zero capacity bytes and can interoperate with 0.9.3. Weighted uses 0x44 and therefore requires 0.9.4 on both endpoints. Revision-1/2/3 values 0x11..0x33 are rejected.

Each direction uses independent AES-GCM keys/counters derived from the PSK handshake transcript. Existing random 32-byte transport keys can be reused. This update does not add TLS PKI, forward secrecy or a security certification.

## DATA and control records

Records retain the existing 40-byte authenticated header and AEAD tag. DATA is at most 32 KiB. Control payloads are empty except RESET_STREAM, which has exactly 8 bytes of big-endian error code. Reliable controls have an independent packet id; a frame's final-size offset is not overloaded as that id.

| Kind | Meaning |
|---:|---|
| 1 OPEN | Lightweight stream identity; no implicit DATA permission. |
| 2 OPEN_OK | Accepts the referenced OPEN. |
| 3 DATA | Logical stream offset, packet id, payload. |
| 4 ACK | Reliable frame receipt. DATA receipt alone is not application consumption. |
| 5 WINDOW | Stream consumed offset and absolute per-stream limit. |
| 6 FIN | Own sending direction's final offset and reliable packet id. |
| 7 | Legacy RST is not accepted. |
| 8 / 9 PING / PONG | Per-carrier liveness and RTT. |
| 10 SESSION_WINDOW | stream=0; consumed total and cumulative MAX_DATA limit. |
| 11 STOP_RECEIVING | Requests peer to stop its sending direction; contains no invented peer final size. |
| 12 RESET_STREAM | Own sending final size, reliable packet id, 8-byte error code. |
| 13 OPEN_REJECT | Rejects a still-unaccepted OPEN by matching OPEN id. |
| 14 CREDIT_PROBE | Regenerates consumption feedback without allocating DATA credit. |
| 15 FINAL_CONSUMED | Reliable confirmation of consumed FIN range before graceful terminal metadata is retired. |

## Weighted scheduling semantics

Weighted changes only the normal TCP DATA capacity prior. When a configured rate exists for the local sending direction, path ETA scoring and the bounded application-layer flight budget use that configured rate instead of learned `goodput`.

It does **not** override live safety evidence. A disconnected path remains unavailable. A path under `penaltyUntil` is avoided while any healthy path exists. Real RTT/minRTT and writer queue debt remain in path scoring. The existing delivery timeout, reinjection and retransmission path remains unchanged.

If upload capacity is omitted, only the Mac→Landing sending direction falls back to the original learned Aggregate capacity and flight budget; the Landing→Mac direction still uses the required download capacity.

## Credit and directionality

Every direction has independent cumulative committed/consumed totals and limits. Sender peer session credit starts at zero until an explicit SESSION_WINDOW arrives. Receive-side commitment counts increases of each stream's highest observed offset, including holes and declared final size. Duplicate or retransmitted byte ranges are never charged twice.

Per-stream WINDOW is monotonic, initially 16 KiB and up to 16 MiB ahead of consumption. For confirmed outstanding `u_i`, actual bootstrap is `sum(min(u_i,16 KiB))` and growth is `sum(max(u_i-16 KiB,0))`; these remain bounded at 32 MiB and 96 MiB across at most 2048 occupied stream identities. Sender MAX_DATA remains an additional 128 MiB cumulative-window gate. Pending DATA has independent byte/frame bounds and bootstrap reservation.

FIN and RESET are independently accounted for each sending direction. Ordinary DATA ACK is delivery, not consumption. Closing identities persist until both directional obligations are settled and continue to count toward the 2048 slot bound. Terminal duplicate-suppression records remain bounded to 8192.

## Memory and error boundaries

Physical receive pages remain independently charged with a 128 MiB page-accounting limit. Sparse pages can hit this limit before DATA credit is exhausted; this is a typed stream resource failure, not permission to tear down the whole session.

Controls remain bounded and reliable/periodically regenerated where applicable. No socket or backend I/O holds `Session.mu`. WINDOW and SESSION_WINDOW never decrease; retransmission does not mint credit. 0.9.4 does not increase any credit, stream-count or physical receive-memory limit.
