# Quick Start

[中文版](QUICKSTART.zh-CN.md)

This guide uses the published v0.9.4 release artifacts. It assumes that you already have reachable TCP Relay endpoints and a Linux amd64 machine that will run Landing.

Release: https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.9.4

## 1. Download and verify

Download at least:

- MPTCP-Desk-0.9.4-universal.dmg
- mptcp-landing
- MPTCP-Desk-0.9.4-SHA256SUMS
- mptcp-landing.sha256

Verify the macOS DMG on macOS:

~~~sh
shasum -a 256 -c MPTCP-Desk-0.9.4-SHA256SUMS
~~~

Verify Landing on Linux:

~~~sh
sha256sum -c mptcp-landing.sha256
~~~

The v0.9.4 release Source-ID is:

~~~text
d4b8f362a8179257f2889abfea587c480755470bac6a78d8c023c3496c66f2bf
~~~

## 2. Prepare Landing

On Linux amd64, run management operations as root or with appropriate sudo privileges:

~~~sh
chmod 755 ./mptcp-landing
./mptcp-landing version
./mptcp-landing menu
~~~

The interactive manager exposes install, config, start, stop, restart, status, logs, doctor, upgrade, rollback and uninstall operations.

For an already managed installation, a controlled upgrade is:

~~~sh
./mptcp-landing upgrade --source ./mptcp-landing --sha256 <trusted-full-sha256>
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
~~~

Do not publish or paste the transport key. Reuse the existing backend, transport-key and Relay/port model unless you intentionally change the deployment.

## 3. Install the macOS client

The DMG contains a Universal arm64/x86_64 build for macOS 13+.

1. Stop the old forwarding session.
2. Open the DMG.
3. Replace the existing MPTCP Desk application in Applications.
4. Launch the app and accept the normal macOS / Keychain prompts.
5. Do not disable SIP or Gatekeeper.

The DMG is ad-hoc signed and not notarized.

## 4. Configure the profile

The Userspace local entry is normally:

~~~text
127.0.0.1:1081
~~~

This is a transparent TCP entry, **not a SOCKS5 server**.

Configure the Relay endpoints and select a scheduler:

- Auto: default and general-purpose.
- Aggregate: force throughput-oriented learned scheduling.
- Protect: force path-role protection.
- Weighted: use known per-Relay capacity.

Weighted requires, for every Relay:

- downstream Mbps: required;
- upstream Mbps: optional.

If upstream is omitted, Mac → Landing uses learned Aggregate capacity for that direction.

## 5. Version compatibility

Weighted requires v0.9.4 on both endpoints.

Auto, Aggregate and Protect in v0.9.4 keep the same scheduler hello bytes as v0.9.3 and can interoperate with v0.9.3.

MPX/2 and older protocol candidates are not compatible with MPX/3 Rev5.

## 6. Verify after start

Check the Landing:

~~~sh
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
~~~

On macOS, check:

- configured scheduler;
- effective scheduler;
- carrier count and connectivity;
- per-path RTT and queue state;
- path roles / penalties;
- Weighted rate fields when Weighted is selected;
- retransmission and lifecycle diagnostics.

For Weighted, a configured capacity does not force a broken path to carry traffic. Disconnect, penalty, queue pressure, RTT, delivery timeout and reinjection still apply.

## 7. Roll back

If a managed Landing upgrade must be reverted:

~~~sh
/usr/local/bin/mptcp-landing rollback
~~~

If you roll Landing back to a version older than 0.9.4, stop using Weighted on the Mac as well.

For more detail, see [Deployment](../userspace/DEPLOYMENT.zh-CN.md), [Scheduler modes](../userspace/SCHEDULER-MODES.md) and [Troubleshooting](TROUBLESHOOTING.zh-CN.md).
