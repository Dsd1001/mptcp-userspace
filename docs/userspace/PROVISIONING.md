# MPX Provisioning 0.10.0

MPX Provisioning is the configuration/control plane for MPTCP Userspace. It is not a data proxy and it does not change MPX/4 Draft 04 data-plane bytes.

0.10.0 keeps the existing Profile API and adds **Client Bundles** so one secret URL can deliver multiple complete runtime configurations.

## Objects

### Profile

A Profile is still one complete authoritative client runtime configuration:

- transport mode (`userspace_multipath` or `native_mptcp` on macOS);
- `listen_port`;
- TCP / UDP switches;
- Auto / Aggregate / Protect / Weighted scheduler;
- 2–8 Relay IPv4/port entries;
- Weighted download/upload capacities;
- MPX transport key;
- background-resident setting.

Each Profile owns its own Relay set, scheduler and transport key. `listen_port` remains Provisioning-controlled; the client does not invent or override it.

### Bundle

A Bundle is an ordered list of 1–32 Profiles plus a runtime policy:

- `single_select` — the client must select exactly one Profile. Different Profiles may reuse the same `listen_port` because they are not active together.
- `parallel` — the client may activate one or more Profiles simultaneously. Every Profile in the Bundle must have a unique `listen_port`.

Parallel Profiles are **independent runtimes**. Each active Profile gets its own local listener, MPX Session, Carrier set, scheduler and transport key. Bundle orchestration never flattens different Profile Relay lists into one Session.

Provisioning validates parallel port uniqueness when a Bundle is saved. A later Profile edit that would make an existing parallel Bundle invalid is rejected. A Profile referenced by any Bundle cannot be deleted until it is removed from that Bundle.

## API schemas

Existing Profile URLs stay compatible:

```text
https://config.example.com/v1/config/<64-hex-secret>
https://config.example.com/v1/config/hkbn-5line/<64-hex-secret>
```

Profile response schema remains `schema_version: 1`.

Bundle URLs are independent credentials:

```text
https://config.example.com/v1/bundle/<64-hex-secret>
https://config.example.com/v1/bundle/hk-main/<64-hex-secret>
```

A Bundle response uses schema 2:

```json
{
  "schema_version": 2,
  "kind": "bundle",
  "bundle_id": "0123456789abcdef",
  "revision": "r3-20261003T120000Z",
  "display_name": "Hong Kong Lines",
  "mode": "parallel",
  "profiles": [
    {
      "schema_version": 1,
      "profile_id": "aaaaaaaaaaaaaaaa",
      "revision": "r4-20261003T115500Z",
      "display_name": "HKBN",
      "mode": "userspace_multipath",
      "listen_port": 1081,
      "scheduler_mode": "weighted",
      "tcp_enabled": true,
      "udp_enabled": true,
      "background_resident": true,
      "transport_key": "<64-hex>",
      "relays": [
        {"host":"43.250.173.88","port":8849,"download_mbps":94,"upload_mbps":20},
        {"host":"43.250.173.83","port":8849,"download_mbps":94,"upload_mbps":20}
      ]
    }
  ]
}
```

The Bundle document embeds complete Profiles so one authoritative fetch is enough to validate and start the selected set.

## Client behavior

### macOS

MPTCP Desk stores the secret Provisioning URL in Keychain. On sync/start it detects schema 1 vs schema 2.

For a Bundle, the UI shows every Profile with its Provisioning-delivered local port and Relay count. The local selection is remembered by non-secret `bundle_id`:

- `single_select`: exactly one Profile is selected;
- `parallel`: one or more Profiles may be selected.

Before a parallel start, the client validates the selected ports again and probes all required TCP/UDP loopback sockets before launching any Profile runtime. If a required port is already occupied, the group does not start. After preflight, the engine starts one independent child runtime per selected Profile and reports aggregate plus per-Profile status.

The authoritative Bundle, including transport keys, is passed ephemerally to the engine. Bundle transport keys are not written to ordinary preferences. Managed mode still refuses to start from stale cached configuration when the authoritative fetch fails.

### Linux

The headless Linux client accepts Bundle JSON with:

```sh
mptcp-client-linux-amd64 validate-bundle [profile-id ...] < bundle.json
mptcp-client-linux-amd64 run-bundle [profile-id ...] < bundle.json
```

It can also fetch a secret Provisioning URL without placing that URL in the process argument list:

```sh
printf '%s\n' '{"url":"https://config.example.com/v1/bundle/hk-main/<secret>","profile_ids":["aaaaaaaaaaaaaaaa","bbbbbbbbbbbbbbbb"]}' \
  | mptcp-client-linux-amd64 validate-managed

printf '%s\n' '{"url":"https://config.example.com/v1/bundle/hk-main/<secret>","profile_ids":["aaaaaaaaaaaaaaaa","bbbbbbbbbbbbbbbb"]}' \
  | mptcp-client-linux-amd64 run-managed
```

For `single_select`, `profile_ids` must resolve to one Profile. For `parallel`, an omitted `profile_ids` selects all Profiles; an explicit subset is also allowed. Linux still supports Userspace MPX/4 only; Native MPTCP is macOS-only.

Client/Landing 0.10.1 changes parallel runtime failure handling only: after atomic port preflight, one failed/unreachable Profile no longer cancels healthy Profile runtimes. Per-Profile error events are emitted and healthy local listeners remain active; all-selected failure still terminates the Bundle. Provisioning schema 2 is unchanged.

Remote managed URLs require HTTPS. HTTP is accepted only for loopback development. Redirects are rejected. Bundle responses are bounded to 512 KiB; legacy single-Profile responses remain bounded to 64 KiB.

## Administration UI

The 0.10.0 administration console has two lists:

- **配置中心** — Profile editor with Basic / Relay / Scheduler / Issuance pages;
- **Client Bundles** — Bundle editor with Profile membership, `single_select` / `parallel` mode, live port-plan diagnostics and independent Bundle API URL controls.

Profile Relay rows support add/copy/reorder/delete. Copy duplicates IP/port/capacity and focuses the IPv4 last octet for convenient same-/24 editing.

Profile and Bundle URLs both support:

- automatic 256-bit random secret;
- optional readable alias plus the random secret;
- manual secret rotation;
- automatic secret rotation when URL mode/alias changes.

Transport keys and API URLs are masked by default in the web UI.

## Administrator password

The System page can change the HTTP Basic Auth administrator password. The request requires the current password. New passwords must be 8–512 characters and cannot contain NUL/newline characters.

Changed passwords are atomically written to `MPX_PROVISION_ADMIN_PASSWORD_FILE` with mode `0600`. If the path is not set, it defaults to `admin-password` beside `MPX_PROVISION_DATA`. The persisted value takes precedence over bootstrap `MPX_PROVISION_ADMIN_PASSWORD` after restart, so the service can remain non-root.

## Quick start

```sh
cd provisioning
go build -o mpx-provision .

export MPX_PROVISION_ADMIN_PASSWORD='choose-at-least-8-characters'
export MPX_PROVISION_LISTEN='127.0.0.1:8088'
export MPX_PROVISION_PUBLIC_BASE_URL='https://config.example.com'
export MPX_PROVISION_DATA='/var/lib/mpx-provision/profiles.json'
# Optional; defaults beside MPX_PROVISION_DATA:
export MPX_PROVISION_BUNDLES='/var/lib/mpx-provision/bundles.json'
export MPX_PROVISION_ADMIN_PASSWORD_FILE='/var/lib/mpx-provision/admin-password'
./mpx-provision
```

Put the service behind a maintained HTTPS reverse proxy. The service intentionally listens on loopback by default.

## Upgrade compatibility

0.10.0 loads existing 0.9.8/0.9.9 Profile records without migration. Existing `/v1/config/<token>` and custom-alias Profile URLs remain valid until explicitly changed or rotated. `bundles.json` is created only when Bundles are saved.

A 0.9.9 admin password file, if already created, remains authoritative after the upgrade.

## Security properties

- Public client credentials are high-entropy bearer secrets in the URL path.
- Configure reverse-proxy access logs to redact/omit both `/v1/config/` and `/v1/bundle/` paths.
- Public API responses use `Cache-Control: no-store`; clients do not follow redirects.
- Production remote client URLs require HTTPS.
- Profile data and Bundle data are written with mode `0600` under a `0700` private directory.
- Administrator password persistence uses an atomic `0600` file and is never returned by APIs or written to normal application logs.
- Bundle selection stored locally by the Mac contains only non-secret Profile IDs; the secret Provisioning URL remains in Keychain.
- Transport keys in a fetched Bundle are used in memory/engine stdin and are not written to ordinary preferences.

## Opaque public response envelope (0.10.2)

The secret Profile and Bundle URLs are unchanged. Public `/v1/config/...` and `/v1/bundle/...` responses now contain only a compact `{v,n,d}` envelope. The existing URL token is decoded as 256 bits and used as the HMAC-SHA256 key over the fixed context `mpx-provision-config-envelope-v1`; that 256-bit result is the AES-256-GCM key. Every response uses a fresh 96-bit random nonce and authenticates the fixed AAD `mpx-provision-envelope-v1`. The encrypted plaintext remains the existing schema 1 Profile or schema 2 Bundle JSON, so the internal control-plane model does not change.

This is intentionally a lightweight opacity layer using the already-secret API URL: it prevents Relay endpoints and transport keys from being immediately readable when the URL is opened, without adding device enrollment or another credential. Anyone who possesses the complete API URL still possesses the decryption secret. HTTPS remains mandatory for non-loopback deployments. 0.10.2 clients also accept legacy plaintext schema 1/2 responses for migration compatibility.
