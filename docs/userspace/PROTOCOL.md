# MPX/4 Protocol Version 4 Stable implementation profile

MPTCP Userspace **1.0.0** implements **MPX/4 Protocol Version 4 Stable** over ordinary TCP Carriers. The frozen protocol boundary is Dsd1001/MPX-4 tag `protocol-v4.0.0`, commit `44f587fd279ed2238b070dd68114c76822353f4d`, with Draft 11 as the stable specification revision.

Provisioning remains a separate configuration/control plane and does not alter MPX/4 data-plane bytes. Every active Profile owns an independent MPX Session and Carrier set.

## Version and compatibility

The wire Protocol Version remains **4**. That number is distinct from the MPTCP Userspace product version and from the Keychain Broker version.

Pre-Stable Draft 04 implementations also used Protocol Version 4 but do not implement the frozen Stable handshake/state semantics. MPTCP Userspace 1.0.0 therefore does **not** silently fall back to Draft 04 on the same listener. A staged migration should use separate old/new listeners or ports.

## Carrier identity and limits

One TCP connection maps to one MPX/4 Carrier. TCP segmentation and read/write call boundaries have no MPX meaning.

Stable MPX/4 defines CARRIER_ID as any non-zero MPX VarInt from 1 through 2^62-1. Carrier IDs may be sparse. `MAX_CARRIERS` is a separate Session capability and limits the number of simultaneously active logical Carriers.

The current product accepts **2–8 configured Relays per Profile** and advertises a local `MAX_CARRIERS=8`. This is an implementation/resource bound, not a wire identifier bound.

Carrier Generation rules include:

- an unused Carrier ID starts at Generation 0;
- replacements use a strictly greater Generation;
- failed or unauthenticated candidates do not advance accepted Generation;
- stale/equal candidates are rejected without mutating the live Session;
- a higher authenticated Generation atomically supersedes the previous incarnation;
- superseded incarnations cannot create protocol state or receive new Attempts;
- Generation never wraps;
- replacement preserves Session-owned Stream, credit, tombstone and Transmission state.

If all Carriers disappear, the Session enters DORMANT for the bounded local retention window. Existing reliable state is retained for recovery; new Stream/DATA commitment is blocked until a Carrier is established again.

## Handshake and security

The implementation supports Stable CREATE and JOIN with:

- 32-byte pre-shared Transport Key;
- HKDF-SHA256 and MPX-Expand-Label;
- HMAC-SHA256 Client/Server Finished;
- AES-256-GCM Secure Records;
- canonical MPX VarInts and strictly ordered Parameters;
- directional `MAX_FRAME_PAYLOAD`, `MAX_RECORD_SIZE` and `MAX_STREAMS`;
- critical Session-wide `MAX_CARRIERS`;
- `VERSION_NEGOTIATION` for unsupported Preface versions;
- classified pre-establishment `HANDSHAKE_REJECT` where a safe response boundary exists;
- unknown optional Parameters ignored and unknown critical Parameters rejected.

CREATE-time Session-scoped limits are immutable. JOIN repeats the endpoint's CREATE-time Session-scoped values; incompatible values are rejected without changing the retained Session.

Unauthenticated candidates never attach application Carrier state to a live Session.

## Frames, Streams and Transmission identity

The Core implementation includes PING/PONG, CARRIER_CLOSE, SESSION_CLOSE, STREAM_OPEN, STREAM_OPEN_OK, STREAM_OPEN_REJECT, STREAM_DATA, TRANSMISSION_ACK, STREAM_CREDIT, STREAM_FIN, RESET_STREAM, STOP_SENDING, STREAM_CONSUMED, **TRANSMISSION_RETIRE**, SESSION_CREDIT and CREDIT_PROBE.

STREAM_DATA is limited to 32768 bytes. Stream byte identity is `(Stream ID, Offset)` and is independent of Carrier identity.

Reliable Frames use Session-wide monotonically allocated Transmission IDs. Retransmission or cross-Carrier reinjection keeps the same Transmission ID. Confirmation type and Stream identity must match the original Transmission.

The sender tracks the contiguous Settled Through prefix and advertises it with TRANSMISSION_RETIRE. The receiver retains replayable confirmations until the peer's retirement watermark permits compaction.

## Flow control and reordering

Application DATA requires both Stream and Session credit. Credit is absolute. Older/stale credit pairs may arrive on another Carrier and are ignored; crossed/non-monotonic pairs are FLOW_CONTROL_ERROR.

Retransmission or reinjection of already committed logical bytes consumes no additional logical credit.

Current 1.0.0 bounds are:

- active peer-initiated Streams: 2048;
- STREAM_DATA payload: 32 KiB;
- per-Stream receive-credit window: 16 MiB;
- Session receive-credit window: 128 MiB;
- physical receive allocation: 128 MiB;
- locally active MPX Carriers: 8;
- bounded sender DATA/control queues.

## Error scope

Stable failure scope is enforced at the candidate, Carrier, Stream or Session boundary as appropriate. In particular:

- safe pre-establishment failures may use HANDSHAKE_REJECT;
- FRAME_ENCODING_ERROR is Carrier-scoped;
- Secure Record authentication/integrity failure terminates the affected Carrier;
- FLOW_CONTROL_ERROR, FINAL_SIZE_ERROR, TRANSMISSION_ID_ERROR and established STREAM_STATE_ERROR are Session-scoped;
- rejecting a JOIN candidate does not mutate the existing Session;
- local Carrier output failure does not reclassify valid peer input as a peer protocol violation.

## Scheduler policy

MPX/4 Stable Core intentionally does **not** negotiate Auto, Aggregate, Protect or Weighted. These names are MPTCP Userspace endpoint-local policies.

The optional published **Carrier Receive Capacity Hint** extension uses Parameter Type `0x40 RECEIVE_CAPACITY_HINT`, CRITICAL=0. It is unilateral, Carrier-scoped and authenticated by the successful handshake transcript. It is scheduling metadata only: not flow-control credit, a bandwidth reservation or a delivery guarantee.

A configured Weighted client can advertise its receive-side estimate so Landing can use it for server-to-client scheduling. Upload capacity remains a local client-side input. Landing also has an independent local scheduler policy.

## UDP

UDP remains the independent authenticated MPU/1 datagram plane with its own path health, receipts, fragmentation/reassembly and scheduling. It is not an MPX/4 Core Datagram extension.

## Interoperability verification

The source tree vendors the **20 frozen Core JSON vector files** from `protocol-v4.0.0`, plus the published capacity-hint extension vector. Tests cover Stable key schedule, Finished authentication, Secure Records, MAX_CARRIERS, VERSION_NEGOTIATION, HANDSHAKE_REJECT, Carrier Generation, DORMANT recovery, confirmation validity, credit reordering, TRANSMISSION_RETIRE and error scope.

For release requirements and evidence classes, see [VALIDATION.md](VALIDATION.md).
