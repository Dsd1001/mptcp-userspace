# MPX/4 Draft 04 implementation profile

MPTCP Userspace 0.9.8 implements MPX/4 Draft 04 over ordinary TCP Carriers. The normative protocol source used for this release is `Dsd1001/MPX-4` commit `5854899b63676eb8bb43048678ef99b4589170c3`.

Provisioning is a separate control/configuration plane and does not alter MPX/4 data-plane bytes.

## Binding

One TCP connection maps to one MPX/4 Carrier. TCP segmentation and write/read call boundaries have no MPX meaning. Every new or replacement Carrier begins with the MPX/4 connection preface and performs the complete authenticated handshake.

Draft 04 keeps the Draft 03 wire format. This release adds the normative replacement state machine:

- the first accepted incarnation of an unused Carrier ID uses Generation 0;
- the Session retains Highest Accepted Generation for every used Carrier ID;
- stale and equal Generation candidates are rejected with `CARRIER_CONFLICT`, including after transport loss;
- a higher Generation is committed only after authenticated establishment;
- a failed candidate does not advance accepted Generation state;
- committing a higher Generation supersedes lower incarnations atomically;
- superseded incarnations cannot receive new Attempts, path samples or create new protocol state;
- Generation values never wrap;
- replacement preserves all Session-owned Stream, credit, tombstone and Transmission state while deriving fresh Carrier traffic secrets and sequence spaces.

## Handshake and security

The implementation supports Draft 04 `CREATE` and `JOIN` using:

- 32-byte pre-shared transport key;
- HKDF-SHA256 and MPX-Expand-Label;
- HMAC-SHA256 Client/Server Finished authentication;
- AES-256-GCM Secure Records;
- canonical Parameter ordering and canonical VarInt encoding;
- directional `MAX_FRAME_PAYLOAD`, `MAX_RECORD_SIZE` and `MAX_STREAMS` limits;
- Session-wide Scheduler negotiation and Carrier-scoped `PATH_CAPACITY` for Weighted.

Unauthenticated candidates never attach application Carrier state to a live Session.

## Frames, Streams and Transmission identity

The implementation supports the Core Frame set including PING/PONG, CARRIER_CLOSE, SESSION_CLOSE, STREAM_OPEN, STREAM_OPEN_OK, STREAM_OPEN_REJECT, STREAM_DATA, TRANSMISSION_ACK, STREAM_CREDIT, STREAM_FIN, RESET_STREAM, STOP_SENDING, STREAM_CONSUMED, SESSION_CREDIT and CREDIT_PROBE.

STREAM_DATA is bounded to 32768 bytes. Stream byte identity is `(Stream ID, Offset)` and is independent of Carrier identity. Reliable Frames use Session-wide monotonically allocated Transmission IDs; every retransmission or reinjection retains the same Transmission ID.

Draft 04 acknowledgement handling distinguishes:

- an outstanding Transmission ID, which is settled;
- a previously settled/compacted ID, which is a harmless stale duplicate;
- a never-allocated future ID, which is `TRANSMISSION_ID_ERROR`;
- a Stream-ID mismatch for an outstanding Transmission, which is also `TRANSMISSION_ID_ERROR`.

## Error scope and closure

v0.9.8 implements the Draft 04 failure scopes:

- Stream-opening `STREAM_LIMIT`, stream-specific `RESOURCE_LIMIT` and the explicit pre-open `STREAM_STATE_ERROR` rule use `STREAM_OPEN_REJECT`;
- `FRAME_ENCODING_ERROR` is Carrier-scoped and uses `CARRIER_CLOSE` when safely reportable;
- authentication/integrity failure terminates the affected Carrier and need not emit a wire error;
- `FLOW_CONTROL_ERROR`, `FINAL_SIZE_ERROR`, `TRANSMISSION_ID_ERROR` and established `STREAM_STATE_ERROR` are Session-scoped and cause `SESSION_CLOSE` when an authenticated writable Carrier is available;
- generic established `PROTOCOL_VIOLATION` is Session-scoped;
- rejecting a JOIN candidate does not modify the existing Session.

`CARRIER_CLOSE` and `SESSION_CLOSE` encode Error Code, Trigger Frame Type and an optional diagnostic UTF-8 reason of at most 256 bytes. Protocol behavior never depends on reason text.

## Flow control

Application DATA requires both Stream and Session credit. Credit is absolute and monotonic. Retransmission/reinjection of already committed bytes consumes no new logical credit. Exceeding advertised Stream or Session credit is `FLOW_CONTROL_ERROR`; contradicting an established final size is `FINAL_SIZE_ERROR`.

Implementation limits remain:

- per-Stream receive-credit window: 16 MiB;
- Session receive-credit window: 128 MiB;
- active Streams: 2048;
- physical receive allocation: 128 MiB;
- bounded sender pending DATA and control state.

## Scheduling

Core Scheduler IDs map directly to:

- 0: Auto
- 1: Aggregate
- 2: Protect
- 3: Weighted

The concrete score, thresholds, queue model and probe cadence are local implementation policy. The Draft 04 semantic constraints are enforced: no scheduling on closing/superseded/unusable Carriers, no Transmission-ID change on another Attempt, no extra logical credit for reinjection, and no Scheduler-ID change inside an existing Session.

For Weighted, `PATH_CAPACITY` is a scheduling input only. Zero uplink capacity means no configured uplink estimate was supplied; the implementation may derive one locally.

TRANSMISSION_ACK receiver timestamps are emitted in microseconds relative to the receiver Session epoch and are used only as same-clock deltas.

## UDP

UDP remains the independent MPU/1 authenticated datagram plane. It has its own path health, receipts, fragmentation/reassembly and per-datagram scheduling. MPX/4 `PATH_CAPACITY` values are not applied to MPU/1.

## Interoperability verification

The 0.9.8 source tree verifies:

- canonical MPX VarInt vectors;
- Frame encoding vectors;
- key-schedule / Finished vectors;
- consecutive AES-256-GCM Secure Record, nonce and AAD vectors;
- official Draft 04 Carrier Generation semantic vectors;
- official Draft 04 Error Scope semantic vectors;
- multi-Carrier Session operation, loss/rejoin, scheduler negotiation, credit, retransmission/reinjection and wrong-key rejection.
