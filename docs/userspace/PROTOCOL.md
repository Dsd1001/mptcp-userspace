# MPX/4 Draft 03 implementation profile

MPTCP Userspace 0.9.7 continues to implement the same MPX/4 Draft 03 wire protocol introduced in 0.9.6, over ordinary TCP Carriers. Provisioning is a control/configuration plane and does not change MPX/4 wire bytes. The normative protocol specification is maintained in https://github.com/Dsd1001/MPX-4.

## Binding

One TCP connection maps to one MPX/4 Carrier. TCP segmentation and write/read call boundaries have no MPX meaning. Every new or replacement Carrier begins with the MPX/4 connection preface and performs a complete authenticated handshake.

A replacement Carrier reuses the logical Carrier ID with a strictly greater Carrier Generation, derives fresh traffic keys and restarts both directional Secure Record sequence spaces at zero.

## Handshake and security

The implementation supports the Draft 03 `CREATE` and `JOIN` flow using:

- 32-byte pre-shared transport key;
- HKDF-SHA256 and MPX-Expand-Label;
- HMAC-SHA256 Client/Server Finished authentication;
- AES-256-GCM Secure Records;
- canonical Parameter ordering and canonical VarInt encoding;
- directional `MAX_FRAME_PAYLOAD`, `MAX_RECORD_SIZE` and `MAX_STREAMS` limits;
- Session-wide Scheduler negotiation and Carrier-scoped `PATH_CAPACITY` for Weighted.

Unauthenticated CREATE/JOIN attempts do not allocate or attach live Session Carrier state.

## Frames and Streams

The implementation maps its existing Stream engine onto MPX/4 Core Frames including STREAM_OPEN, STREAM_DATA, TRANSMISSION_ACK, STREAM_CREDIT, STREAM_FIN, RESET_STREAM, STOP_SENDING, STREAM_CONSUMED, SESSION_CREDIT, CREDIT_PROBE and PING/PONG.

STREAM_DATA remains bounded to 32768 bytes. Stream byte ordering is by Stream offset, independent of Carrier order. Reliable Frames use Session-wide monotonically allocated Transmission IDs; retransmission or reinjection keeps the same logical Transmission identity.

## Flow control

Application DATA requires both Stream and Session credit. Credit is absolute and monotonic. Retransmission/reinjection of already committed bytes does not consume new logical credit.

Implementation limits remain:

- per-Stream receive-credit window: 16 MiB;
- Session receive-credit window: 128 MiB;
- active Streams: 2048;
- physical receive allocation: 128 MiB;
- sender pending DATA: bounded independently from receive allocation.

## Scheduling

MPX/4 scheduler IDs map directly to the existing policies:

- 0: Auto
- 1: Aggregate
- 2: Protect
- 3: Weighted

Scheduler selection remains an implementation decision after negotiation. Path state includes RTT, minimum RTT, measured delivery rate, outstanding/queued work, penalty state and configured capacity where applicable.

TRANSMISSION_ACK Receiver Timestamp values are emitted in microseconds as defined by Draft 03.

## UDP

UDP is intentionally outside the MPX/4 Core implementation in 0.9.6. The existing independent MPU/1 authenticated datagram plane remains available and maintains its own path measurements, receipts, fragmentation/reassembly and scheduling. MPX/4 Weighted `PATH_CAPACITY` values are not applied to MPU/1.

## Interoperability verification

The 0.9.6 source tree includes tests that reproduce the public Draft 03 vectors for:

- canonical VarInt;
- Frame encoding;
- key schedule and Finished values;
- consecutive AES-256-GCM Secure Records and nonces.

End-to-end tests also cover single/multiple Carriers, Stream multiplexing, path failure/rejoin, scheduler negotiation, flow control, retransmission/reinjection and wrong-key rejection.
