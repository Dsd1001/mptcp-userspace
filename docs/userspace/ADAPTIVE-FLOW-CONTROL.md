# Flow control — MPTCP Userspace v1.1.1

This document explains the current Stream/Session credit model. It replaces older 0.x shared-credit/admission descriptions for normal v1.1.1 operation.

## Protocol credit

The peer advertises two independent limits:

- a **Stream WINDOW** for each Stream;
- a **Session WINDOW** for aggregate DATA committed in the Session.

The sender can transmit DATA only when both have room.

Current hard receive-side limits are:

```text
Per-Stream maximum window: 16 MiB
Session receive credit:    128 MiB
Physical receive account:  128 MiB
```

## v1.1.1 authoritative send rule

The authoritative send allowance is derived from the peer's published MPX windows:

```text
stream room  = peerLimit(stream)  - txNext(stream)
session room = peerLimit(session) - txCommitted(session)
allowance    = min(stream room, session room, local resource room)
```

The old local `txUsed` / `txGrowth` accounting no longer acts as a second 128 MiB send-credit pool.

This matters under concurrency: aggregate Session consumption can advance even while per-Stream consumed replay/diagnostic accounting is temporarily behind. v1.1.1 avoids turning that lag into artificial Session-wide head-of-line blocking.

## What remains local

Removing the legacy send-credit admission layer does not mean the sender is unbounded.

Local hard resource controls include:

- pending frame count;
- **1 GiB** pending DATA-byte pool;
- bootstrap pending-capacity reserve so new active Streams can enqueue initial DATA;
- Carrier flight/budget controls;
- receiver memory/page accounting;
- Stream/Session lifecycle limits.

These protect memory and fairness. They are not peer flow-control credit.

## Receive-side window management

The receiver tracks application consumption and publishes additional Stream/Session credit as resources are consumed and become reusable.

Per-Stream receive windows can grow up to 16 MiB. Session aggregate credit remains bounded at 128 MiB. The implementation also tracks physical receive allocation so advertised credit cannot turn into unbounded page retention.

## Telemetry

Compatibility telemetry can still expose legacy fields such as:

- `txUsed`;
- `txGrowth`;
- bootstrap/growth counters;
- writer-turn counters.

In v1.1.1 these must be interpreted as diagnostics/compatibility mirrors, not as the protocol authority for whether DATA may be sent.

Normal blocking reasons should instead map to real gates such as:

- `stream_window_or_open`;
- `session_window`;
- `pending_frames`;
- `pending_bytes`;
- Carrier budget/availability.

## Performance troubleshooting

If throughput plateaus:

1. determine whether Stream WINDOW is exhausted;
2. determine whether Session WINDOW is exhausted;
3. check pending frame/byte saturation;
4. check Carrier flight/queue/outstanding;
5. check Session/Stream lock telemetry;
6. check Relay/Landing CPU;
7. verify **Relay BBR** and **Landing CUBIC** before changing flow-control constants.

Do not increase MPX windows merely to compensate for lower-layer TCP queueing or CPU bottlenecks.
