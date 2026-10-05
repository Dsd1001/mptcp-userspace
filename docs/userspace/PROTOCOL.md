# MPX/4 Draft 04 implementation profile

MPTCP Userspace **0.10.7** implements MPX/4 Draft 04 over ordinary TCP Carriers. The normative protocol source tracked by this implementation is Dsd1001/MPX-4 commit 5854899b63676eb8bb43048678ef99b4589170c3. **0.10.7 intentionally keeps the exact MPX/4 Draft 04 wire/state/scheduler semantics shipped in 0.10.5. This patch release refreshes the macOS and Provisioning interfaces; signed updates and remote device control remain as shipped in 0.10.6. The protocol draft does not advance.**

Provisioning is a separate configuration/control plane and does not alter MPX/4 data-plane bytes. Every active Profile owns an independent MPX Session and Carrier set.

## Binding and current Carrier bound

One TCP connection maps to one MPX/4 Carrier. TCP segmentation and read/write call boundaries have no MPX meaning.

The current implementation accepts Carrier IDs **1–8** and configures **2–8 Relays per Profile**. This is an implementation bound; the project does not claim that every MPX/4 field is intrinsically limited to eight values.

Every new or replacement Carrier begins with the MPX/4 preface and completes the authenticated handshake.

Draft 04 Carrier Generation behavior includes:

- first accepted incarnation of an unused Carrier ID uses Generation 0;
- Highest Accepted Generation is retained for every used Carrier ID;
- stale/equal Generation candidates are rejected with CARRIER_CONFLICT;
- a higher Generation commits only after authenticated establishment;
- failed candidates do not advance accepted Generation;
- replacement atomically supersedes lower incarnations;
- superseded incarnations cannot create new protocol state or receive new Attempts;
- Generation never wraps;
- replacement preserves Session-owned Stream, credit, tombstone and Transmission state.

## Handshake and security

The implementation supports Draft 04 CREATE and JOIN with:

- 32-byte pre-shared Transport Key;
- HKDF-SHA256 and MPX-Expand-Label;
- HMAC-SHA256 Client/Server Finished;
- AES-256-GCM Secure Records;
- canonical Parameter ordering and canonical VarInt encoding;
- directional MAX_FRAME_PAYLOAD, MAX_RECORD_SIZE and MAX_STREAMS;
- Session-wide Scheduler negotiation;
- Carrier-scoped PATH_CAPACITY for Weighted.

Unauthenticated candidates never attach application Carrier state to a live Session.

## Frames, Streams and Transmission identity

The Core implementation includes PING/PONG, CARRIER_CLOSE, SESSION_CLOSE, STREAM_OPEN, STREAM_OPEN_OK, STREAM_OPEN_REJECT, STREAM_DATA, TRANSMISSION_ACK, STREAM_CREDIT, STREAM_FIN, RESET_STREAM, STOP_SENDING, STREAM_CONSUMED, SESSION_CREDIT and CREDIT_PROBE.

STREAM_DATA is limited to 32768 bytes. Stream byte identity is (Stream ID, Offset) and is independent of Carrier identity.

Reliable Frames use Session-wide monotonically allocated Transmission IDs. Retransmission or cross-Carrier reinjection keeps the same Transmission ID.

## Error scope

Draft 04 failure scopes are enforced:

- stream-opening STREAM_LIMIT / RESOURCE_LIMIT and pre-open STREAM_STATE_ERROR use STREAM_OPEN_REJECT;
- FRAME_ENCODING_ERROR is Carrier-scoped;
- authentication/integrity failure terminates the affected Carrier;
- FLOW_CONTROL_ERROR, FINAL_SIZE_ERROR, TRANSMISSION_ID_ERROR and established STREAM_STATE_ERROR are Session-scoped;
- generic established PROTOCOL_VIOLATION is Session-scoped;
- rejecting a JOIN candidate does not mutate the existing Session.

CARRIER_CLOSE and SESSION_CLOSE carry the registered Error Code, Trigger Frame Type and optional bounded diagnostic text.

## Flow control and implementation limits

Application DATA requires both Stream and Session credit. Credit is absolute and monotonic. Retransmission/reinjection of already committed logical bytes consumes no new logical credit.

Current v0.10.7 bounds:

- active peer-initiated Streams: 2048;
- STREAM_DATA payload: 32 KiB;
- per-Stream receive-credit window: 16 MiB;
- Session receive-credit window: 128 MiB;
- physical receive allocation: 128 MiB;
- bounded sender DATA/control queues.

## Scheduling

Core Scheduler IDs map to Auto, Aggregate, Protect and Weighted. The exact score, thresholds, probe cadence and queue model are implementation policy.

The implementation preserves Draft 04 invariants: no scheduling on unusable/superseded Carriers, no Transmission-ID change on reinjection, no extra logical credit for retransmission, and no Scheduler-ID change inside an existing Session.

Weighted PATH_CAPACITY is a scheduling input only. It is authenticated as part of the Carrier handshake and does not create flow-control credit or a delivery guarantee.

## UDP

UDP remains the independent authenticated MPU/1 datagram plane with its own path health, receipts, fragmentation/reassembly and scheduling. It is not an MPX/4 Core Datagram extension.

## Interoperability verification

The current tree verifies canonical VarInt/Frame encoding, key schedule and Finished authentication, AES-256-GCM Secure Records, Draft 04 Carrier Generation and Error Scope vectors, multi-Carrier Session operation, loss/rejoin, scheduler negotiation, credit, retransmission/reinjection and wrong-key rejection.

For current release validation, see [VALIDATION.md](VALIDATION.md).
