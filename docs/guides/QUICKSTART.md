# Quick Start

[中文版](QUICKSTART.zh-CN.md)

This guide targets **MPTCP Userspace v0.10.4 / MPX/4 Draft 04**.

## 1. Download and verify

Download the required artifacts from the [v0.10.4 release](https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.10.4).

Typical files:

- MPTCP-Desk-0.10.4-universal.dmg
- mptcp-client-linux-amd64 or mptcp-client-linux-arm64
- mptcp-landing or mptcp-landing-linux-arm64
- mpx-provision or mpx-provision-linux-arm64 when managed configuration is needed
- MPTCP-Userspace-0.10.4-SHA256SUMS

Verify hashes before installing.

## 2. Prepare Landing

On Linux:

~~~sh
chmod 755 ./mptcp-landing
./mptcp-landing version
./mptcp-landing menu
~~~

Keep the Landing configuration, backend, transport key and Relay topology private. For an existing installation, back up the current binary/config before replacing the binary and restart the systemd service only after verifying the release hash.

The recommended deployment pair is 0.10.4 Client + 0.10.4 Landing.

## 3. Install the client

### macOS

Open the Universal DMG and install MPTCP Desk. The current release is ad-hoc signed and not Developer ID notarized.

The home page lets you choose:

- **Local configuration** — enter the runtime configuration directly;
- **Remote configuration** — save a secret Provisioning Profile/Bundle URL.

### Linux

~~~sh
chmod 755 ./mptcp-client-linux-amd64
./mptcp-client-linux-amd64 version
./mptcp-client-linux-amd64 doctor-userspace
~~~

Linux supports Userspace MPX/4 only; Native MPTCP fallback is macOS-only.

## 4. Local configuration

A current Userspace Profile includes:

- listen_port, normally a loopback entry such as 1081;
- TCP/UDP switches;
- Auto / Aggregate / Protect / Weighted scheduler;
- 2–8 Relay IPv4/port entries;
- 64-hex-character Transport Key;
- Weighted capacity values when Weighted is selected.

The local listener is a transparent TCP entry, not a SOCKS5 server.

## 5. Managed Provisioning

Provisioning can issue either one Profile or a Bundle containing 1–32 Profiles.

For macOS, paste the secret URL under **Remote configuration**, save it and perform the first sync. That first successful sync creates a persistent Last Known Good cache. Later starts, app/system restarts and sleep/wake recovery launch immediately from the matching cache while the API refreshes in the background; the client does not wait for the API timeout.

For Linux, keep the URL out of process arguments:

~~~json
{"url":"https://config.example.com/v1/bundle/<secret>","profile_ids":["profile-a","profile-b"]}
~~~

~~~sh
mptcp-client-linux-amd64 validate-managed < managed.json
mptcp-client-linux-amd64 run-managed < managed.json
~~~

Remote URLs require HTTPS. Public responses from 0.10.2+ Provisioning are opaque encrypted envelopes; the 0.10.4 client decrypts them automatically.

## 6. Parallel Bundles

Parallel startup first validates that all selected local listen ports are unique and available. That preflight is atomic.

After preflight, Profile runtimes are independent. If one Profile cannot reach/authenticate its Landing, that Profile reports an error while healthy Profile listeners continue running. The Bundle stops only when every selected Profile is unavailable or the user stops it.

## 7. Verify operation

In MPTCP Desk, open **Path Diagnostics** and check:

- Profile runtime status;
- configured/effective scheduler;
- connected paths;
- RTT / Goodput / queue / outstanding;
- retransmits and reorder/pending counters.

For remote Profiles, Relay endpoints are intentionally hidden from the customer UI.

See [Troubleshooting](TROUBLESHOOTING.zh-CN.md) and [Deployment / rollback](../userspace/DEPLOYMENT.zh-CN.md) for more detail.
