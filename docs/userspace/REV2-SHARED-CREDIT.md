# MPX/3 capability revision 5 (0.9.4)

## Scope and baseline

This revision fixes starvation caused by retaining monotonic per-stream WINDOW entitlements in a shared receive-credit reservation ledger. It is not a replacement path scheduler. Rev5 adds Weighted, but the shared-credit and directional-finalization model in this document remains unchanged. Baseline: 0.8.0 Source-ID 6da891a037e6478d0e04b835689a2a50237d2ec0bb7b12874b91f0d146e0972a.

The preliminary B experiment is NOT production code: it only accounted lifetime bytes after the first 16 KiB, omitted receive-side validation, and did not settle resets. Its bootstrap unit test also exhausted the stream window, so its beyond-bootstrap check did not independently prove growth blocking. Production tests must remove those confounders.

## Per-direction accounting

Every direction is independent. Stream WINDOW is a monotonic absolute offset with a 16 KiB initial target and 16 MiB maximum forward span. It is an entitlement, never a session reservation.

SESSION_WINDOW has stream=0, offset=cumulative consumed/discarded offset credit, id=absolute cumulative MAX_DATA. It starts at 128 MiB and advances with consumption; sender peer limit starts at zero until this authenticated control message arrives. Lower/reordered limits cannot revoke credit. Limits and consumed offsets are overflow checked. It is regenerated after consumption and periodically, without requiring a blocked message.

The sender debits first commitment of a contiguous logical byte range, before adding the payload to the reliable pending ledger. Sending or reinjecting an existing ledger entry never debits again. Ordinary DATA/FIN ACK is delivery, not consumption. Stream WINDOW consumption or an acknowledged RESET final size releases the per-stream send usage.

Receiver accounting charges increases of each stream's highest declared offset, including holes and FIN/RESET final size. Duplicate/overlapping data never charges the same offset range twice. Sum of these increases must not exceed the last session limit. Consumption/discard advances a monotonic cumulative total. Actual pages are tracked separately.

## New-stream protection and fairness

For sender-confirmed unconsumed commitment u_i = txNext_i - peerConsumed_i:

- bootstrap used = sum(min(u_i, 16 KiB)), at most 32 MiB across 2048 streams;
- growth used = sum(max(u_i - 16 KiB, 0)), at most 96 MiB;
- total used at most 128 MiB, additionally bounded by peer MAX_DATA.

These are sliding outstanding bytes, not historical grants or lifetime first bytes. An idle stream with u_i=0 consumes neither pool even if its WINDOW entitlement remains large. Do not lend the bootstrap reserve to bulk in this revision.

The receiver checks the equivalent invariant using rxHigh_i-rxRead_i. Pending bytes/frames also reserve room for bootstrap so DATA metadata cannot undermine the credit guarantee. When growth credit or pending storage is scarce, eligible writers receive FIFO bounded DATA turns. New/sliding bootstrap remains eligible. When every current waiter has room for a full DATA frame, no artificial write-turn wait is introduced; the existing DATA dispatcher still round-robins ready streams. Blocked writers never hold up eligible writers. Already committed data cannot be revoked: a stalled consumer can still hold its legitimate outstanding bytes until consumed or terminated.

At most 2048 active plus unresolved closing stream identities are admitted. Closing identities retain their charged bytes and slot until both directional final-size/consumption obligations are settled; this prevents churn from minting unbounded bootstrap. 2049th admission is a typed stream rejection. OPEN does not wait on DATA credit.

## Directional termination

- FIN: reliable packet id, own sending final size in offset.
- STOP_RECEIVING: reliable packet id, error code in offset; asks the peer to end ITS sending direction and does not invent a final size.
- RESET_STREAM: reliable packet id, own sending final size in offset, exactly 8 payload bytes containing an error code. A RESET receiver checks final size, accounts previously unseen offset credit, discards the remainder once, then ACKs. Its ACK may settle sender usage to that exact final size.
- OPEN_REJECT is only valid for a still-unopened, zero-send stream and echoes the OPEN id. It is not a generic reset.

On local full Close, outgoing commitment is finalized locally and incoming STOP is requested independently. Late frames are handled in closing state; resources are not refunded merely because the application closed. Final size cannot change or fall below an observed offset. Terminal metadata is bounded to 8192 entries; very old retired identities are ignored without reallocating or recrediting. Final-size inconsistency is detected while the terminal record is retained. Unknown stream messages cannot create unlimited state.

FINAL_CONSUMED reliably confirms that the entire received FIN range was consumed. Its ACK is required before normal receive-final metadata can be retired; this prevents a lost last WINDOW plus terminal-record eviction from leaving a permanent sender-credit tail. It has an independent reliable packet id, final offset and no payload.

CREDIT_PROBE regenerates a stream WINDOW, including for a terminal identity; it carries no DATA and cannot grant fabricated credit. It recovers a final consumption update lost after the peer retired a stream. Closing controls remain bounded and retryable. A lost carrier is not permission to refund unacknowledged application commitment.

## Wire compatibility

Authenticated hello scheduler byte is 0x41/0x42/0x43 for Auto/Aggregate/Protect and 0x44 for Weighted. Weighted adds authenticated fixed-size direction capacities in hello bytes 40..43; old three modes keep those bytes zero. Shared-credit/final-size controls and MPX/3 AEAD framing remain mandatory. Weighted requires 0.9.4 on both endpoints; Relay remains an opaque TCP forwarder.

## Memory and error scope

DATA flow credit is NOT a promise that 128 MiB of sparse payload will fit in the current allocator. Each 32 KiB page is charged approximately 40 KiB. The independent physical receive-page budget is 128 MiB. A page allocation failure aborts only the affected stream through directional final-size settlement; healthy streams/session/carriers continue. Malformed authenticated flow-control/final-size messages are protocol errors, not ordinary page exhaustion.

## Verification and rollout

Preserve old reports and releases. Test credit saturation with stream credit still available; test first and resumed-small-flow bootstrap, pending reservation, repeated/reordered feedback, overflow, ACK versus consumption, reset/final-size races, duplicate/late DATA, multi-carrier reinjection, bounded terminals, fragmented pages and 2048/2049 admission. Run race/vet, real engine stdin mode tests, both UI architectures, time-matched 0/8/16/32 warmed-idle A/B, 128/256/512/1024/2048 for 30 seconds twice and full high-BDP twice. Preserve all samples and original performance thresholds. Offscreen UI and shaped loopback tests are not WAN App+Surge verification. Never auto-replace production HKT.

Reference for the accounting/termination principles (not an assertion of QUIC interoperability): RFC 9000 sections 3.5, 4.1, 4.4 and 4.5, https://www.rfc-editor.org/rfc/rfc9000.html .
