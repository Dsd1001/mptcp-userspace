# Scheduler modes — MPTCP Userspace v1.1.1

MPTCP Userspace exposes four endpoint-local scheduling policies: **Auto, Aggregate, Protect and Weighted**.

They are implementation policy, not MPX/4 Stable Core wire Scheduler IDs. Client and Landing can choose independently. For most production deployments, using the same mode on both endpoints is easier to understand because it produces similar policy in both traffic directions.

## Common invariants

All modes must obey the same protocol/resource rules:

- only usable authenticated Carriers are eligible;
- Stream and Session WINDOWs remain hard send gates;
- local pending frame/byte limits remain hard resource gates;
- a scheduling choice cannot rewrite Transmission IDs;
- retransmission/reinjection must preserve MPX reliability semantics;
- superseded/closed Carrier generations are not made eligible by policy.

Scheduler policy cannot override protocol correctness.

## Auto

Auto is the recommended starting mode.

It uses live path state to balance useful aggregation with protection against unhealthy paths. It is intended for deployments where exact path capacity is not known or where path quality changes over time.

Use Auto when you want the system to decide how aggressively to spread traffic based on current feedback.

## Aggregate

Aggregate favors concurrent use of multiple eligible Carriers.

It is useful when paths are independently useful and the operator explicitly wants to combine their capacity. It still respects path health, flow-control and local flight/budget limits; it does not mean every frame is round-robined equally.

## Protect

Protect prioritizes stability when one or more paths are behaving badly.

Unhealthy paths can be demoted from normal DATA service while retaining bounded probing/recovery opportunities. Protect is appropriate when latency spikes, loss or intermittent paths are more damaging than leaving some nominal capacity unused.

## Weighted

Weighted combines configured directional capacity with live feedback.

Inputs include the product's capacity prior and runtime path signals such as:

- RTT;
- measured delivery/goodput;
- queue/outstanding state;
- penalty and path health;
- disconnect/timeout history;
- bounded feedback timing.

The configured Mbps values are **not fixed traffic percentages**. A path configured as 100 Mbps can receive less traffic than a 50 Mbps path if current feedback shows the first path is congested or unhealthy.

### Directional capacities

A local Profile can provide `download_mbps` and optional `upload_mbps` per Relay. Download capacity is the normal required capacity input for Weighted operation; upload can be provided when known.

Capacity hints are scheduling information, not MPX flow-control credit.

### v1.1.5: control-frame routing around a blocked Carrier writer

Weighted tracks when a Carrier's existing encrypted TCP write has been blocked
for longer than max(100 ms, 2 times minimum measured RTT) and preferentially
routes new path-independent control frames and reliable stream controls
(OPEN/FIN/RESET) to another eligible, unblocked Carrier. A directed DATA ACK,
PING or PONG retains its established Carrier affinity. If every Carrier is
blocked, control delivery still has a fallback; nothing is dropped merely
because every writer is busy.

This is a control-plane latency improvement for partial path stalls, not a
change to DATA scheduling, configured capacity, Flight Budget, Queue Admission,
Stream/Session WINDOWs or MPX/4 stable wire semantics. It does not claim to
resolve full-path congestion collapse under simultaneous 150-stream downloads.
Auto, Aggregate and Protect retain their previous routing behavior.


## Which mode should I use?

| Goal | Suggested mode |
| --- | --- |
| General production default | **Auto** |
| Maximize healthy multi-path use | **Aggregate** |
| Isolate unstable paths aggressively | **Protect** |
| Known asymmetric path capacities | **Weighted** |

## Congestion-control baseline

Scheduler behavior depends on the feedback produced by underlying TCP Carriers. The recommended host baseline is:

- **Landing = CUBIC**;
- **Relay = BBR + `fq` preferred**.

If a scheduler appears unstable, verify this baseline and inspect loaded RTT/queue before changing scheduler constants. Bottom-layer queue growth can look like a scheduler problem.

## Diagnostics

When comparing modes, record at least:

- effective scheduler mode;
- connected/eligible path count;
- path role/reason;
- RTT;
- measured goodput;
- queue and outstanding bytes;
- retransmits/reinjection;
- Stream/Session WINDOW waits;
- Relay/Landing CPU.

Use repeated tests and compare the same topology. One peak speed-test result is not sufficient evidence for a scheduler change.
