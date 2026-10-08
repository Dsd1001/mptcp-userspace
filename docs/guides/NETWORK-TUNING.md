# Network tuning — Landing CUBIC / Relay BBR

This guide defines the recommended Linux TCP baseline for MPTCP Userspace v1.1.1.

## Recommended baseline

| Host role | Congestion control | Queue discipline | Recommendation |
| --- | --- | --- | --- |
| **Landing** | **CUBIC** | keep the distro default unless testing shows a reason to change it | Recommended default |
| **Relay** | **BBR** | **fq** preferred | Recommended default |
| Backend | workload-specific | workload-specific | Usually leave unchanged |
| macOS Client | system-managed | system-managed | Do not try to force Linux sysctls |

This split is a deployment recommendation, not an MPX/4 protocol requirement.

## Why Relay uses BBR

A Relay sits on a Carrier edge and sends ordinary TCP on one or both forwarding legs. In heterogeneous paths, BBR's pacing and bandwidth/RTT model can reduce persistent queues and make Relay egress less dependent on loss-driven sawtooth behavior. That is useful when the MPX scheduler is already comparing path RTT, queue, outstanding data and delivery signals.

Pair BBR with `fq` when possible so pacing is applied cleanly by the host scheduler.

## Why Landing uses CUBIC

Landing is the convergence endpoint for all active Carriers. The project recommendation is to keep this host on CUBIC so the convergence point has conventional loss-based TCP behavior while MPX/4 handles the multipath scheduling and per-Carrier flight logic above it.

Do not default Landing to BBR simply because Relay uses BBR. Stacking a model-based controller at the convergence endpoint can change queue/RTT feedback seen by MPX scheduling and can make multi-path behavior harder to interpret. If a deployment wants to test BBR on Landing, treat it as an A/B experiment and compare against the CUBIC baseline.

## Check kernel support first

On every Linux host:

```sh
sysctl net.ipv4.tcp_available_congestion_control
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
```

A Relay intended to use BBR must list `bbr` in `tcp_available_congestion_control`.

If BBR is a loadable module:

```sh
sudo modprobe tcp_bbr
sysctl net.ipv4.tcp_available_congestion_control
```

If `bbr` still does not appear, do not force the setting; use a kernel that provides BBR or keep the existing controller until the host can be upgraded safely.

## Apply the Landing baseline

Temporary runtime change:

```sh
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic
```

Verify:

```sh
sysctl net.ipv4.tcp_congestion_control
```

For persistence, create a dedicated sysctl file such as:

```text
# /etc/sysctl.d/90-mptcp-userspace-landing.conf
net.ipv4.tcp_congestion_control = cubic
```

Then load it using the distribution's normal sysctl mechanism, for example:

```sh
sudo sysctl --system
```

The Landing recommendation does not require changing `default_qdisc`. Keep the distribution default unless measured queueing justifies a separate qdisc change.

## Apply the Relay baseline

Temporary runtime change:

```sh
sudo modprobe tcp_bbr
sudo sysctl -w net.core.default_qdisc=fq
sudo sysctl -w net.ipv4.tcp_congestion_control=bbr
```

Verify:

```sh
sysctl net.ipv4.tcp_available_congestion_control
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
```

Persistent example:

```text
# /etc/sysctl.d/90-mptcp-userspace-relay.conf
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
```

If the kernel requires module loading at boot, use the distribution-supported modules-load mechanism, for example a file containing `tcp_bbr` under `/etc/modules-load.d/`.

## What these settings affect

Linux TCP congestion control applies to TCP sockets for which that host is the sender. In this project that includes Carrier TCP legs and, on Landing, ordinary backend TCP sockets. UoT also rides the TCP Carrier Session and therefore inherits Carrier TCP behavior.

Native MPU/1 UDP does not use TCP congestion control.

## Validate after changing the host

Change one role at a time and record a before/after sample. At minimum check:

```sh
sysctl net.ipv4.tcp_congestion_control
ss -s
ss -ti
```

Then inspect MPTCP Userspace diagnostics for:

- Carrier RTT;
- measured goodput;
- queue and outstanding bytes;
- retransmits / reinjection;
- path role and penalty;
- Stream and Session WINDOW waits;
- CPU usage on Relay and Landing.

A congestion-control change is not successful merely because a speed test peaks higher. Reject a change that creates sustained queue growth, much higher loaded RTT, retransmit instability or CPU saturation.

## Do not mix many tuning changes at once

Start from:

1. Landing = CUBIC;
2. Relay = BBR + `fq`;
3. default socket buffer autotuning;
4. unchanged MTU unless the path requires a known fix.

Only after this baseline is stable should you experiment with socket buffers, qdisc variants, pacing, NIC offload or other sysctls. Keep each experiment reversible and measured.

## Rollback

Runtime rollback examples:

```sh
# Landing
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic

# Relay fallback example
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic
```

For a persistent rollback, remove or edit the dedicated `/etc/sysctl.d/90-mptcp-userspace-*.conf` file and reload sysctls. Record the previous qdisc before changing it so it can also be restored if required.
