# MPX Provisioning 0.9.9

MPTCP Desk supports a managed mode in which the Mac stores only one secret Provisioning API URL. Every time forwarding starts (including background recovery), the client fetches and validates the complete current configuration before starting the transport engine.

The managed configuration covers the settings that would otherwise be entered on the main client screen:

- transport mode (`userspace_multipath` or `native_mptcp`);
- local listen port;
- TCP / UDP switches;
- scheduler mode;
- 2–8 Relay IPv4/port entries;
- Weighted download/upload capacities;
- MPX transport key;
- background-resident setting.

The repository includes a small self-hosted Provisioning service with an embedded administration web page under `provisioning/`.

## Quick start

Build and run the service behind HTTPS:

```sh
cd provisioning
go build -o mpx-provision .

export MPX_PROVISION_ADMIN_PASSWORD='use-a-random-password-at-least-24-characters'
export MPX_PROVISION_LISTEN='127.0.0.1:8088'
export MPX_PROVISION_PUBLIC_BASE_URL='https://config.example.com'
export MPX_PROVISION_DATA='/var/lib/mpx-provision/profiles.json'
# optional; defaults to a sibling admin-password file next to MPX_PROVISION_DATA
export MPX_PROVISION_ADMIN_PASSWORD_FILE='/var/lib/mpx-provision/admin-password'
./mpx-provision
```

The service intentionally listens on loopback by default. Put Caddy, nginx or another TLS reverse proxy in front of it for remote clients. MPTCP Desk rejects clear-text remote endpoints; HTTP is accepted only for localhost development.

A Docker example is also included:

```sh
cd provisioning
cp docker-compose.example.yml docker-compose.yml
# edit the admin password and public HTTPS base URL
docker compose up -d --build
```

The example publishes the container only on host `127.0.0.1:8088`; expose it remotely through your HTTPS reverse proxy, not by changing that binding to a public interface without equivalent protection.

Open:

```text
https://config.example.com/admin
```

The browser will ask for HTTP Basic authentication. The default username is `admin`; it can be changed with `MPX_PROVISION_ADMIN_USER`. `MPX_PROVISION_ADMIN_PASSWORD` is the bootstrap password. If a persisted admin password file exists, it takes precedence on restart.

## Administration page

The 0.9.9 administration UI uses a profile sidebar with second-level pages for Basic settings, Relay paths, Scheduling/transport, and Issuance/security. Each profile owns its own Relay list. Relay rows can be added, copied, reordered or removed; Copy duplicates address/port/capacity and focuses the IPv4 last octet for fast same-/24 editing.

The **System** page can change the administrator Basic Auth password. The user must enter the current password plus a new password of at least 8 characters. A successful change immediately invalidates the old password and atomically writes the new password to `MPX_PROVISION_ADMIN_PASSWORD_FILE` with mode `0600`. The service does not need root privileges for this: by default the password file is stored next to `MPX_PROVISION_DATA`, which should already be a private service-writable `0700` directory. The persisted password takes precedence over the bootstrap environment password after restart.

A newly created profile receives a 32-byte random URL secret. Automatic URLs keep the existing form:

```text
https://config.example.com/v1/config/4c...64-hex-characters...
```

The secret is the client credential. 0.9.9 also supports an optional unique custom alias while retaining the random secret, for example `https://config.example.com/v1/config/hkbn-5line/<64-hex-secret>`. Changing URL mode or alias automatically rotates the secret so an old URL cannot become valid again later. Manual secret rotation preserves the alias but immediately invalidates the previous URL. Existing 0.9.8 token-only records remain valid after upgrade until explicitly changed.

## Client API response

A current Userspace response looks like:

```json
{
  "schema_version": 1,
  "revision": "r4-20261003T012345Z",
  "display_name": "HKBN 5-Line",
  "mode": "userspace_multipath",
  "listen_port": 1081,
  "scheduler_mode": "weighted",
  "tcp_enabled": true,
  "udp_enabled": true,
  "background_resident": true,
  "transport_key": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "relays": [
    {"host":"43.250.173.88","port":8849,"download_mbps":94,"upload_mbps":20},
    {"host":"43.250.173.83","port":8849,"download_mbps":94}
  ]
}
```

For Native mode, `scheduler_mode` and `transport_key` are omitted.

## Mac behavior

Paste the generated URL into **配置来源 → Provisioning API URL** and save it. In managed mode the local configuration editors are hidden; the view shows only a summary of the last successfully fetched profile.

Pressing **启动** performs this sequence:

1. read the secret API URL from Keychain;
2. GET the current profile using an ephemeral URL session;
3. reject redirects, non-HTTPS remote URLs, non-200 responses, responses larger than 64 KiB and invalid profile data;
4. validate all normal MPTCP Desk profile constraints;
5. write a Userspace transport key to the existing Keychain item when required;
6. store only the key-free ordinary profile in `UserDefaults`;
7. apply the optional background-resident setting;
8. start the engine.

If the authoritative API fetch fails, managed mode **does not start using stale cached configuration**. Existing cached values are left untouched only for diagnostics and for switching back to manual mode.

## Security properties

- Production client endpoints require HTTPS.
- The client does not follow HTTP redirects for profile fetches.
- The high-entropy API URL is stored in Keychain and is not included in engine stdin, ordinary preferences or diagnostic logs.
- The MPX transport key remains in the existing transport Keychain item and is stripped from normal profile persistence.
- Public API responses include `Cache-Control: no-store` and `Pragma: no-cache`.
- The administration page and administration JSON endpoints require Basic authentication. Password changes require both a currently authenticated request and the current password in the request body.
- The persisted administrator password file is atomically replaced with mode `0600`; the Profile data directory remains `0700`. Password values are not returned by APIs or written to normal application logs.
- The service data file contains API tokens and transport keys and is written with mode `0600`; its directory is created with mode `0700`.
- The API token lives in the URL path. Configure the TLS reverse proxy to suppress or redact access logs for `/v1/config/` so bearer tokens are not retained in ordinary request logs.
- Keep the service itself on loopback and terminate public TLS at a maintained reverse proxy unless you deliberately provide equivalent transport security another way.
- The client credential is in the `/v1/config/<token>` path. Configure the reverse proxy access log to redact or omit that path so tokens are not written to ordinary web logs.
