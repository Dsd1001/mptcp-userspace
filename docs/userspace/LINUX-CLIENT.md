# Linux client

MPTCP Userspace 0.9.8 publishes a headless Linux client for both `amd64` and `arm64`. It reuses the same Go MPX/4 Draft 04 transport engine as MPTCP Desk; there is no separate Linux protocol implementation.

## Supported mode

The Linux client supports **`userspace_multipath` only**. The macOS-only Native MPTCP fallback is deliberately unavailable on Linux.

Published binaries:

```text
mptcp-client-linux-amd64
mptcp-client-linux-arm64
```

Both are `CGO_ENABLED=0` static Linux ELF binaries.

## Configuration

The client accepts the same schema-3 Userspace profile used by the macOS engine. Configuration is supplied as one JSON object on stdin.

Example:

```json
{
  "schema_version": 3,
  "mode": "userspace_multipath",
  "listen_port": 1081,
  "tcp_enabled": true,
  "udp_enabled": true,
  "transport_key": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "scheduler_mode": "weighted",
  "relays": [
    {"host":"192.0.2.10","port":8849,"download_mbps":100,"upload_mbps":20},
    {"host":"198.51.100.20","port":8849,"download_mbps":100,"upload_mbps":20}
  ]
}
```

Validate without starting:

```sh
cat profile.json | ./mptcp-client-linux-amd64 validate
```

Check the binary identity:

```sh
./mptcp-client-linux-amd64 version
./mptcp-client-linux-amd64 doctor-userspace
```

Run:

```sh
cat profile.json | ./mptcp-client-linux-amd64 run
```

The local transparent TCP entry remains `127.0.0.1:<listen_port>`. If UDP is enabled, the independent MPU/1 datagram entry uses the same loopback port over UDP.

## Service example

A minimal systemd pattern is:

```ini
[Unit]
Description=MPTCP Userspace Linux Client
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/bin/sh -c 'exec /usr/local/bin/mptcp-client-linux-amd64 run < /etc/mptcp-userspace/profile.json'
Restart=on-failure
RestartSec=3
LimitNOFILE=16384
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

On arm64, replace the binary name with `mptcp-client-linux-arm64`.

Protect `/etc/mptcp-userspace/profile.json` because it contains the MPX transport key. The current Linux CLI does not persist the profile or transport key itself.

## Provisioning

The 0.9.8 Provisioning web/API service is published for both Linux `amd64` and `arm64`. The integrated secret-URL Managed Mode remains a MPTCP Desk/macOS UI feature in 0.9.8; the headless Linux client consumes the validated schema-3 JSON profile on stdin.
