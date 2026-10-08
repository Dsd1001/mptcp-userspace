# Linux Client — MPTCP Userspace v1.1.1

The headless Linux Client uses the same Go MPX/4 Protocol Version 4 Stable engine as MPTCP Desk.

Published builds:

```text
mptcp-client-linux-amd64
mptcp-client-linux-arm64
```

The release builds are static `CGO_ENABLED=0` ELF binaries.

## Commands

```text
version
doctor-userspace
validate
run
validate-bundle [profile-id ...]
run-bundle [profile-id ...]
validate-managed
run-managed
```

`version` prints version, Source-ID and MPX protocol metadata.

## Local Profile

A local userspace Profile is read from stdin:

```sh
./mptcp-client-linux-amd64 validate < profile.json
./mptcp-client-linux-amd64 run < profile.json
```

Example schema-3 Profile:

```json
{
  "schema_version": 3,
  "mode": "userspace_multipath",
  "listen_port": 1081,
  "tcp_enabled": true,
  "udp_enabled": true,
  "transport_key": "REPLACE_WITH_64_HEX_CHARACTERS",
  "relays": [
    {"host":"192.0.2.10","port":24001,"download_mbps":50.0},
    {"host":"198.51.100.20","port":24001,"download_mbps":50.0}
  ],
  "scheduler_mode": "auto"
}
```

The local listener is `127.0.0.1:<listen_port>`. The Client does not auto-assign that port.

## Carrier count

The current implementation accepts 1–8 active Carrier addresses for a userspace Session. Carrier IDs themselves use the larger MPX VarInt namespace.

## Bundle

A managed/local Bundle can run Profiles in either:

- `single_select` mode;
- `parallel` mode.

For parallel mode, selected local listener ports must be unique and bindable. The Client performs an atomic local preflight before starting any selected child runtime.

After preflight, Profile runtimes are isolated. One Profile can fail to connect/authenticate while other listeners remain available.

## Automatic reconnect

Each failed parallel Profile retries independently:

```text
1s -> 2s -> 5s -> 10s -> 30s -> every 30s
```

A successful listening state resets that Profile's backoff. An all-down parallel Bundle remains supervised and waits for recovery instead of exiting immediately.

## Managed Provisioning

`validate-managed` and `run-managed` read the control document from stdin so the bearer URL does not need to appear in process arguments:

```json
{
  "url": "https://config.example.com/v1/bundle/<secret>",
  "profile_ids": ["profile-a", "profile-b"]
}
```

```sh
./mptcp-client-linux-amd64 validate-managed < managed.json
./mptcp-client-linux-amd64 run-managed < managed.json
```

Remote URLs require HTTPS. Loopback HTTP is accepted for development. Redirects are rejected.

Treat the complete URL as a bearer credential. Persist it only in a restricted file such as mode `0600`.

## systemd example

```ini
[Unit]
Description=MPTCP Userspace Client
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/bin/sh -c 'exec /usr/local/bin/mptcp-client-linux-amd64 run-managed < /etc/mptcp-userspace/managed.json'
Restart=on-failure
RestartSec=3
LimitNOFILE=16384

[Install]
WantedBy=multi-user.target
```

Keep `/etc/mptcp-userspace/managed.json` readable only by the intended service/root account.

## Linux Client host TCP tuning

The project's **Landing=CUBIC / Relay=BBR** recommendation is specifically about Landing and Relay roles. A Linux Client may have its own network requirements; do not automatically copy the Relay sysctl profile onto every Client.

Tune the Client only after measuring it as a distinct endpoint.
