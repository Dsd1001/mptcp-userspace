# Scheduler modes — v0.10.4 / MPX/4 Draft 04

MPTCP Userspace currently exposes four configured scheduler policies: **Auto, Aggregate, Protect and Weighted**.

The Scheduler identifier is authenticated as part of the MPX/4 Session/Carrier handshake. A live Session does not silently switch to a different configured policy.

## Aggregate

Aggregate is the baseline multi-Carrier DATA selector. It considers live path state such as RTT, measured delivery rate, queue/outstanding work, path budget and penalty state.

A connected Carrier is not guaranteed equal traffic. The selector chooses the path with the lowest current delivery cost that still has usable flight/queue capacity.

## Weighted

Weighted uses the same safety/path-cost logic but replaces the normal measured-rate capacity term with configured directional PATH_CAPACITY when available.

- download_mbps is required;
- upload_mbps is optional;
- capacity is authenticated in the handshake;
- configured capacity is not a packet ratio;
- configured capacity is not flow-control credit;
- configured capacity is not a bandwidth guarantee.

RTT, queue, penalty, disconnect, delivery timeout and reinjection protection remain active.

## Protect

Protect restricts degraded paths instead of continuing unrestricted normal DATA scheduling.

Path roles include:

- LEARNING — insufficient delivery evidence;
- ACTIVE — eligible normal path;
- PROBE — bounded qualification traffic;
- BACKUP — normally excluded from ordinary DATA and periodically probed.

The implementation uses bounded probe debt and recovery hysteresis so a path does not oscillate rapidly between healthy and degraded states.

## Auto

Auto is a policy selector. It starts from aggregate behavior and can enter Protect behavior after stable evidence of degradation. Recovery requires healthy evidence over multiple epochs/time, not one transient sample.

Auto does not treat a newly connected LEARNING path as proof that the whole Session is degraded.

## Path signals

Depending on mode, the implementation uses:

- connected/active state;
- base/current RTT;
- measured goodput;
- writer queue;
- outstanding flight;
- path budget;
- delivery samples;
- penalty/timeout state;
- configured PATH_CAPACITY;
- path role and probe state.

## Diagnostics

MPTCP Desk exposes configured/effective mode, mode switches, path role/reason, RTT, Goodput, queue, outstanding, retransmits and resource accounting.

Remote Bundle diagnostics keep these values independently per Profile while hiding Relay endpoint addresses.

## Limits and evidence

The current v0.10.4 implementation supports up to 8 Carriers per MPX Session. Scheduler performance at higher path counts is not implied by the current release.

Source-matched release evidence lives in SCHEDULER-MODES.json, TESTS.json, PROVENANCE.json, CAPACITY.json and RUNTIME.json inside the release package.
