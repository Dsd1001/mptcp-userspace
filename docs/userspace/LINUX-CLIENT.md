# Linux client — MPTCP Userspace 0.10.3

The headless Linux Client reuses the same Go Userspace MPX/4 engine as MPTCP Desk. Published artifacts are available for linux/amd64 and linux/arm64.

Linux supports userspace_multipath. Native MPTCP fallback remains macOS-only.

## Artifacts

~~~text
mptcp-client-linux-amd64
mptcp-client-linux-arm64
~~~

The release binaries are CGO_ENABLED=0 static ELF builds.

## Commands

~~~text
version
doctor-userspace
validate
run
validate-bundle [profile-id ...]
run-bundle [profile-id ...]
validate-managed
run-managed
~~~

version reports release, Source-ID and MPX protocol metadata.

## Local Profile

A local schema-3 Profile can be validated or run from stdin:

~~~sh
mptcp-client-linux-amd64 validate < profile.json
mptcp-client-linux-amd64 run < profile.json
~~~

The listener is 127.0.0.1:<listen_port>. The client does not auto-assign the port.

## Bundle

A schema-2 Bundle contains 1–32 complete Profiles:

~~~sh
mptcp-client-linux-amd64 validate-bundle < bundle.json
mptcp-client-linux-amd64 run-bundle < bundle.json
~~~

For parallel mode, omitting IDs selects all Profiles; an explicit subset can also be supplied. For single_select, exactly one Profile is active.

Before a parallel start, all selected local TCP/UDP ports are validated and probed atomically. Local port conflicts abort the group before any child runtime starts.

After preflight, Profile runtimes are independent. If one Profile cannot connect/authenticate or later exits, healthy Profile listeners continue running. The parent terminates only when all selected Profiles are unavailable or the run is cancelled.

## Managed Provisioning

validate-managed and run-managed read a small control document from stdin:

~~~json
{
  "url": "https://config.example.com/v1/bundle/<secret>",
  "profile_ids": ["profile-a", "profile-b"]
}
~~~

~~~sh
mptcp-client-linux-amd64 validate-managed < managed.json
mptcp-client-linux-amd64 run-managed < managed.json
~~~

The URL may refer to a Profile or Bundle.

Remote URLs require HTTPS; loopback HTTP is permitted for development. Redirects are rejected. Single-Profile responses are bounded to 64 KiB and Bundle responses to 512 KiB.

0.10.3 automatically decrypts the opaque v/n/d Provisioning envelope introduced in 0.10.2 and also accepts legacy plaintext schema-1/schema-2 responses for migration.

Treat the managed URL as a bearer credential. If persisted, protect the input file with restrictive permissions such as 0600.

## systemd example

~~~ini
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
~~~

Keep managed.json root/service-readable only.
