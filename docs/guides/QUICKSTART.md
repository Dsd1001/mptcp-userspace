# Quick Start — MPTCP Userspace v1.1.1

This guide brings up one v1.1.1 MPX/4 deployment from Client through Relay to Landing.

## 1. Topology

A normal production path is:

```text
MPTCP Desk / Linux Client
       | ordinary TCP Carrier 1 -> Relay A --+
       | ordinary TCP Carrier 2 -> Relay B --+--> Landing --> backend
       | ordinary TCP Carrier 3 -> Relay C --+
```

The Relay is an opaque forwarder. The Client and Landing are the MPX/4 endpoints.

## 2. Download and verify v1.1.1

Download the required v1.1.1 release assets. The common files are:

```text
MPTCP-Desk-1.1.1-universal.dmg
mptcp-client-linux-amd64
mptcp-client-linux-arm64
mptcp-landing
mptcp-landing-linux-arm64
MPTCP-Userspace-1.1.1-SHA256SUMS
```

Verify the SHA256 file before replacing production binaries.

## 3. Prepare Landing

The Landing binary has an interactive Chinese installer when run with no arguments, or it can be installed non-interactively.

A Landing configuration is schema version 1 and contains the MPX listener, backend, Transport Key, session limit and local scheduler policy:

```json
{
  "schema_version": 1,
  "listen_tcp": "0.0.0.0:24001",
  "listen_udp": "0.0.0.0:24001",
  "backend_tcp": "127.0.0.1:8388",
  "backend_udp": "127.0.0.1:8388",
  "udp_enabled": true,
  "uot_enabled": false,
  "transport_key": "REPLACE_WITH_64_HEX_CHARACTERS",
  "max_sessions": 4,
  "scheduler_mode": "auto"
}
```

`max_sessions` must be 1–16. The private configuration must be a regular `0600`/`0400` file unless it is supplied through the managed systemd credential path.

Install and start:

```sh
sudo ./mptcp-landing install --config ./landing.json --yes --start
sudo /usr/local/bin/mptcp-landing status
sudo /usr/local/bin/mptcp-landing doctor
```

Do not expose the backend port as the MPX listener. The Landing listener and backend must be separate endpoints.

## 4. Set Landing to CUBIC

The recommended production baseline is **CUBIC on Landing**:

```sh
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic
sysctl net.ipv4.tcp_congestion_control
```

Persist it using the host's sysctl configuration, for example:

```text
# /etc/sysctl.d/90-mptcp-userspace-landing.conf
net.ipv4.tcp_congestion_control = cubic
```

See [Network tuning](NETWORK-TUNING.md) before changing any additional TCP settings.

## 5. Prepare each Relay and set it to BBR

Configure the Relay software or forwarding service so each public Carrier endpoint forwards opaque TCP bytes to the Landing TCP listener.

The recommended Relay baseline is **BBR + `fq`**:

```sh
sudo modprobe tcp_bbr
sudo sysctl -w net.core.default_qdisc=fq
sudo sysctl -w net.ipv4.tcp_congestion_control=bbr
sysctl net.ipv4.tcp_available_congestion_control
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
```

Do not put the MPX Transport Key on Relay nodes.

## 6. Create a Client Profile

A local userspace Profile is schema version 3. Example:

```json
{
  "schema_version": 3,
  "mode": "userspace_multipath",
  "listen_port": 1081,
  "tcp_enabled": true,
  "udp_enabled": true,
  "transport_key": "SAME_64_HEX_KEY_AS_LANDING",
  "relays": [
    {
      "host": "192.0.2.10",
      "port": 24001,
      "download_mbps": 50.0,
      "upload_mbps": 20.0
    },
    {
      "host": "198.51.100.20",
      "port": 24001,
      "download_mbps": 50.0
    }
  ],
  "scheduler_mode": "weighted"
}
```

The current implementation supports 1–8 active Carrier addresses. For Weighted mode, directional capacities are priors used with live path feedback; they are not fixed traffic shares.

### macOS

Install `MPTCP-Desk-1.1.1-universal.dmg`, add a local Profile or managed Provisioning URL, and start it from the app.

### Linux

Validate first:

```sh
./mptcp-client-linux-amd64 version
./mptcp-client-linux-amd64 doctor-userspace
./mptcp-client-linux-amd64 validate < profile.json
```

Run:

```sh
./mptcp-client-linux-amd64 run < profile.json
```

The local application listener is `127.0.0.1:<listen_port>`.

## 7. Verify the full path

Before throughput testing, confirm:

1. Landing is active and listening on the intended TCP port.
2. Landing reports CUBIC.
3. Each Relay can reach Landing.
4. Each Relay reports BBR and preferably `fq`.
5. The Client can reach each Relay public endpoint.
6. Client and Landing use the same Transport Key.
7. Multiple Carrier entries become connected in diagnostics.
8. The backend is reachable from Landing.

For Linux host evidence:

```sh
ss -s
ss -ti
```

For Landing logs:

```sh
sudo /usr/local/bin/mptcp-landing logs
```

## 8. Choose a scheduler

Use **Auto** as the default starting point. Use Aggregate when you explicitly want multiple healthy Carriers used aggressively, Protect when the deployment prioritizes isolating bad paths, and Weighted when you know the directional capacity of the paths.

For production, keeping the Client and Landing on the same scheduler mode is usually easier to reason about, although MPX/4 does not require the modes to match.

## 9. UDP choice

Native UDP and UoT are separate product paths:

- enable native UDP when the relay/deployment supports the UDP path;
- enable UoT when UDP should be carried inside the authenticated TCP Carrier Session.

Do not expect Linux TCP congestion-control changes to affect native UDP.

## 10. Next steps

- [Network tuning](NETWORK-TUNING.md)
- [Architecture](ARCHITECTURE.zh-CN.md)
- [Deployment and rollback](../userspace/DEPLOYMENT.zh-CN.md)
- [Troubleshooting](TROUBLESHOOTING.zh-CN.md)
- [Scheduler modes](../userspace/SCHEDULER-MODES.md)
