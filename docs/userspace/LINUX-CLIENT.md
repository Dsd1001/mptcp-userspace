# Linux client — MPTCP Userspace 0.10.0

0.10.0 publishes the headless Userspace MPX/4 client for Linux `amd64` and `arm64`. It reuses the same Go transport engine as MPTCP Desk. Linux supports `userspace_multipath`; Native MPTCP fallback remains macOS-only.

## Artifacts

```text
mptcp-client-linux-amd64
mptcp-client-linux-arm64
```

They are `CGO_ENABLED=0` static ELF binaries.

## Single Profile

The existing schema-3 engine config remains supported:

```sh
mptcp-client-linux-amd64 validate < profile.json
mptcp-client-linux-amd64 run < profile.json
```

The local listener is always `127.0.0.1:<listen_port>`. `listen_port` is part of the configuration supplied by Provisioning/profile JSON; the client does not auto-assign it.

## Provisioning Bundle

A Bundle schema-2 document embeds 1–32 complete Profiles.

Validate or run the Bundle directly:

```sh
mptcp-client-linux-amd64 validate-bundle < bundle.json
mptcp-client-linux-amd64 run-bundle < bundle.json
```

For a `parallel` Bundle, omitting Profile IDs starts all Profiles. A subset can be selected by ID:

```sh
mptcp-client-linux-amd64 validate-bundle aaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbb < bundle.json
mptcp-client-linux-amd64 run-bundle aaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbb < bundle.json
```

For `single_select`, exactly one Profile must be active. If no ID is supplied, the first Profile is selected.

Every simultaneously active Profile is an independent MPX Session with its own listen port, Relay set, scheduler and transport key. Before any child runtime starts, the parent validates unique ports and probes all required loopback TCP/UDP sockets. An occupied port aborts the whole group.

## Fetch directly from Provisioning

`run-managed` and `validate-managed` read a small control JSON object on stdin so a secret API URL does not need to appear in `ps` output:

```json
{
  "url": "https://config.example.com/v1/bundle/hk-main/<secret>",
  "profile_ids": ["aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"]
}
```

```sh
mptcp-client-linux-amd64 validate-managed < managed.json
mptcp-client-linux-amd64 run-managed < managed.json
```

The URL may be either a legacy Profile URL (`/v1/config/...`) or a Bundle URL (`/v1/bundle/...`). Remote URLs require HTTPS, redirects are rejected, legacy Profile responses are bounded to 64 KiB, and Bundle responses are bounded to 512 KiB.

Protect the managed input file if you persist it: the URL is a bearer credential. Prefer restrictive permissions such as `0600`.

## systemd example

For a non-secret schema-3 Profile file:

```ini
[Unit]
Description=MPTCP Userspace Client
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/bin/sh -c 'exec /usr/local/bin/mptcp-client-linux-amd64 run < /etc/mptcp-userspace/profile.json'
Restart=on-failure
RestartSec=3
LimitNOFILE=16384

[Install]
WantedBy=multi-user.target
```

For managed mode, the same pattern can pipe a root/service-readable `0600` control file into `run-managed`.

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

`version` reports the release, Source-ID, MPX wire version and capability revision. `doctor-userspace` verifies the local Userspace runtime prerequisites without connecting to Provisioning.
