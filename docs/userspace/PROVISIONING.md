# MPX Provisioning 0.10.5

MPX Provisioning is the optional configuration/control plane for MPTCP Userspace. It is **not** a data proxy and does not change MPX/4 Draft 04 data-plane bytes.

## Objects

### Profile

A Profile is one complete authoritative runtime configuration:

- transport mode;
- listen_port;
- TCP / UDP switches;
- Auto / Aggregate / Protect / Weighted scheduler;
- 2–8 Relay IPv4/port entries;
- optional Weighted upload/downlink capacity values;
- MPX Transport Key;
- background-resident setting.

Each Profile owns its own local listener, Relay set, Scheduler, Transport Key and MPX Session.

### Bundle

A Bundle contains 1–32 Profiles and one runtime policy:

- **single_select** — exactly one Profile is active; different Profiles may reuse the same listen_port;
- **parallel** — one or more Profiles may run simultaneously; selected Profiles must use unique local listen ports.

Parallel Profiles are independent runtimes. Their Relay lists are never flattened into one Session.

Provisioning rejects invalid Bundle membership/port plans. The Client repeats validation and performs an atomic local socket preflight before starting child runtimes.

After preflight, remote runtime failures are isolated: one failed/unreachable Profile reports its own error while healthy Profile listeners keep running.

## Public URLs and schemas

Profile and Bundle URLs remain high-entropy bearer credentials:

~~~text
https://config.example.com/v1/config/<64-hex-secret>
https://config.example.com/v1/config/<alias>/<64-hex-secret>

https://config.example.com/v1/bundle/<64-hex-secret>
https://config.example.com/v1/bundle/<alias>/<64-hex-secret>
~~~

The decrypted inner payload still uses:

- schema 1 for a single Profile;
- schema 2 with kind=bundle for a Bundle.

The public HTTP response, however, is **not readable configuration JSON** in current releases.

## Opaque response envelope

Since 0.10.2, public Profile/Bundle responses are wrapped in a compact encrypted envelope:

~~~json
{"v":1,"n":"<nonce>","d":"<ciphertext>"}
~~~

The existing 256-bit URL secret is used as key material. HMAC-SHA256 over the fixed context mpx-provision-config-envelope-v1 derives the AES-256-GCM key. Each response uses a fresh 96-bit random nonce and authenticates the fixed AAD mpx-provision-envelope-v1.

The encrypted plaintext is still the existing schema-1 Profile or schema-2 Bundle document, so the internal control-plane model remains simple.

This layer is intentionally lightweight: it prevents Relay endpoints and Transport Keys from being immediately readable when a secret URL is opened in a browser, without adding device enrollment, a public-key infrastructure or a second credential.

**Possession of the complete URL still grants decryption capability.** HTTPS remains mandatory for remote use.

0.10.5 clients also accept legacy plaintext schema-1/schema-2 responses for migration.

## 0.10.5 managed client cache

After the first successful managed sync, MPTCP Desk persists the Last Known Good Profile/Bundle response under the user's Application Support/MPTCPDesk directory. The cache file is mode 0600 and contains the endpoint SHA-256 fingerprint, fetch time, selected Profile IDs and the last validated response bytes. The full Provisioning URL is not written to this file and remains in Keychain.

A matching cache becomes the startup source. Normal start, app/system restart and sleep/wake recovery can launch immediately from it without waiting for the Provisioning request timeout. API refresh runs asynchronously.

A successful refresh updates the cache for the next reconnect and does not restart the active runtime. The cache has no TTL. Each successful sync schedules another check 48 hours later; failures retain the old cache and retry after 1 minute, 5 minutes, 30 minutes and then every 3 hours. A changed Provisioning URL cannot consume a cache created for the previous URL.

With current encrypted Provisioning responses, the cached response bytes remain the opaque v/n/d envelope. Legacy plaintext responses are still accepted for migration and are protected by the local 0600 file permission.

## macOS behavior

MPTCP Desk stores the secret Provisioning URL in Keychain.

The home page explicitly separates:

- Local configuration;
- Remote configuration.

For a Bundle, the Client remembers the selected Profile IDs by non-secret bundle_id. Transport Keys are used from the authoritative response and engine stdin; they are not copied into ordinary preferences.

If a matching Last Known Good cache exists, managed startup uses it immediately and treats the API as an asynchronous update source. Only first use, or a newly changed URL with no matching cache, requires a successful authoritative fetch before startup.

Remote Bundle diagnostics are shown per Profile. The customer UI hides Relay IP/port and raw endpoint errors while still showing scheduler, RTT, Goodput, queue, outstanding, retransmit, reorder and resource state.

## Linux behavior

The Linux Client can fetch a Profile or Bundle without placing the secret URL in process arguments:

~~~json
{"url":"https://config.example.com/v1/bundle/<secret>","profile_ids":["profile-a","profile-b"]}
~~~

~~~sh
mptcp-client-linux-amd64 validate-managed < managed.json
mptcp-client-linux-amd64 run-managed < managed.json
~~~

Remote URLs require HTTPS. Loopback HTTP is accepted for development. Redirects are rejected.

Response limits remain:

- single Profile: 64 KiB;
- Bundle: 512 KiB.

## Administration UI

The admin console contains:

- **Profiles / 配置中心** — Profile editor with Relay, scheduler, issuance and runtime settings;
- **Client Bundles** — Bundle membership, single_select/parallel mode, port-plan validation and independent Bundle URL controls;
- **System** — service/version information and administrator password management.

Profile and Bundle URLs support:

- random 256-bit secret;
- optional readable alias plus secret;
- secret rotation;
- automatic rotation when URL mode/alias changes.

Transport Keys and URLs are masked by default.

## Security and storage

- Configure reverse-proxy access logs to omit/redact /v1/config/ and /v1/bundle/ paths.
- Public configuration responses use Cache-Control: no-store.
- Profile/Bundle data files should be mode 0600 under a private directory.
- Administrator password persistence is atomic and mode 0600.
- Never expose the full URL or Transport Key in public logs/screenshots.
- Provisioning is a control plane; application traffic never passes through it.

## Upgrade compatibility

0.10.5 keeps the existing Profile/Bundle data model and URL format. Existing records and URLs remain valid unless explicitly edited/rotated.

When upgrading from a Provisioning version before 0.10.2, upgrade clients to 0.10.2+ before switching the server to encrypted envelope responses.

For the current release, use a matched **0.10.5 Client + 0.10.5 Landing + 0.10.5 Provisioning** suite.
