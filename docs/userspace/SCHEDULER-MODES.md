# Scheduler modes — v1.0.0 / MPX/4 Protocol Version 4 Stable

MPTCP Userspace exposes four configured scheduler policies: **Auto, Aggregate, Protect and Weighted**.

These are **local implementation policies**, not MPX/4 Stable Core negotiation state. Client and Landing may use different local policies without making the Stable handshake incompatible. A JOIN cannot change the retained endpoint's local scheduler policy.

## Aggregate

Aggregate is the baseline multi-Carrier DATA selector. It considers live path state such as RTT, measured delivery rate, queue/outstanding work, path budget and penalty state.

A connected Carrier is not guaranteed equal traffic. The selector chooses a usable path according to current delivery cost and flight/queue capacity.

## Weighted

Weighted keeps the same safety/path-cost checks while adding configured directional capacity as a local prior.

- Client `download_mbps` is required by the product UI.
- Client `upload_mbps` is optional; omission means automatic estimation.
- Upload capacity remains local to the client sender.
- For server-to-client scheduling, a client may advertise the published MPX/4 `RECEIVE_CAPACITY_HINT` extension.
- The hint is optional, unilateral and CRITICAL=0.
- A peer that does not implement the extension can ignore it and still interoperate at Core.
- Capacity is not a packet ratio, flow-control credit, reservation or throughput guarantee.

RTT, queue, penalty, disconnect, delivery timeout and reinjection protection remain active.

## Protect

Protect restricts degraded paths instead of continuing unrestricted normal DATA scheduling.

Path roles include LEARNING, ACTIVE, PROBE and BACKUP. Bounded probe debt and recovery hysteresis prevent rapid oscillation and keep failed/degraded paths from carrying ordinary DATA until they requalify.

## Auto

Auto is a local policy selector. It starts from Aggregate behavior and can enter Protect after stable evidence of degradation. Recovery requires healthy evidence over multiple epochs/time, not one transient sample.

Landing defaults to Auto. If Auto receives an authenticated `RECEIVE_CAPACITY_HINT`, it may use that hint as local evidence and select Weighted behavior for the direction it sends.

## Path signals

Depending on mode, the implementation may use connected state, RTT, measured delivery rate, writer queue, outstanding flight, path budget, delivery samples, timeout/penalty state, locally configured capacity, authenticated receive-capacity hints and path role/probe state.

No scheduling decision may make an unusable or superseded Carrier eligible, change a Transmission ID during reinjection or override MPX/4 flow control.

## Limits and evidence

The product supports up to 8 simultaneously active Carriers per Session while Stable CARRIER_ID itself spans the full non-zero MPX VarInt space.

Source-matched release evidence lives in `SCHEDULER-MODES.json`, `TESTS.json`, `PROVENANCE.json`, `CAPACITY.json` and `RUNTIME.json`. v1 evidence uses **scheduler policy revision 6**; it is deliberately not described as an authenticated wire scheduler capability.

The Stable laboratory gate compares repeated Auto/Aggregate/Protect results with the frozen 0.10.12 source on the same host and harness. Uniform and heterogeneous candidate medians must retain at least 90% of the matching baseline medians. This is a regression check, not a WAN throughput guarantee.
