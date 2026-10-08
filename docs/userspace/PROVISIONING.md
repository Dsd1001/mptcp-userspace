# Provisioning and remote device control — v1.1.1

Provisioning is the optional control plane for distributing MPTCP Userspace Profiles/Bundles and managing opted-in MPTCP Desk devices. Application payload traffic does **not** pass through Provisioning.

## Deployment model

Recommended production layout:

```text
Internet HTTPS
     |
 nginx / Caddy
     |
127.0.0.1:<provision-port>
     |
 mpx-provision
```

Keep the Provisioning process on loopback/private service networking and terminate public TLS in a mature reverse proxy.

## Profile and Bundle URLs

A public configuration URL is a high-entropy bearer credential, for example:

```text
https://config.example.com/v1/config/<64-hex-secret>
https://config.example.com/v1/config/<alias>/<64-hex-secret>
https://config.example.com/v1/bundle/<64-hex-secret>
https://config.example.com/v1/bundle/<alias>/<64-hex-secret>
```

Possession of the complete URL grants configuration access and decryption capability. Never publish these URLs in logs, screenshots or issues.

## Opaque response envelope

Public Profile/Bundle responses use an encrypted envelope:

```json
{"v":1,"n":"<nonce>","d":"<ciphertext>"}
```

The URL secret is used as key material for the envelope derivation. The plaintext inside remains the validated Profile/Bundle control document.

The envelope reduces accidental disclosure in browsers/proxies but **does not replace HTTPS** because anyone holding the full URL also has the material needed to decrypt the response.

Current clients retain legacy plaintext response compatibility only for migration.

## Bundle modes

A Bundle can be:

- **single_select** — one Profile active at a time;
- **parallel** — one or more Profiles active concurrently.

Parallel Profiles remain independent runtimes. Their Relay lists are not flattened into one Session.

Provisioning validates membership and local port plans. The Client validates again and performs local socket preflight before starting a parallel set.

Runtime failures are isolated after startup: one failed Profile does not stop healthy Profile listeners.

## Last Known Good on macOS

After the first successful managed sync, MPTCP Desk keeps a protected Last Known Good cache associated with the source endpoint fingerprint.

The cache allows normal start, app/system restart and sleep/wake recovery to start from the last validated configuration without waiting on a fresh network request. API refresh runs asynchronously.

A successful refresh updates the next-reconnect configuration and does not forcibly interrupt the current Session. A changed Provisioning URL cannot reuse a cache associated with the previous source fingerprint.

The full Provisioning URL remains in Keychain rather than the ordinary cache file.

## Linux managed mode

Linux Client reads a managed control document from stdin:

```json
{"url":"https://config.example.com/v1/bundle/<secret>","profile_ids":["profile-a"]}
```

```sh
mptcp-client-linux-amd64 validate-managed < managed.json
mptcp-client-linux-amd64 run-managed < managed.json
```

Remote HTTP is rejected; loopback HTTP is allowed for development. Redirects are rejected.

## Device control

Remote device control is separate from Profile/Bundle configuration access and is opt-in on the Mac.

The user must locally:

1. enable Remote Management;
2. configure the HTTPS control-server root URL;
3. enter a short-lived pairing code.

Provisioning cannot remotely enable this switch or replace the locally chosen control-server address.

After pairing, MPTCP Desk uses outbound HTTPS long polling, so the Mac does not need a public IP or inbound port.

The control plane exposes bounded product operations such as:

- desired running/stopped state;
- Profile/Bundle assignment;
- configuration sync generation;
- forwarding restart generation;
- signed-app update generation.

There is no arbitrary Shell command field.

## Device credentials

Each paired Mac gets a unique device credential. The Mac stores its secret in Keychain; Provisioning stores a hash in `devices.json`. Pairing codes are also persisted as hashes rather than plaintext secrets.

Admin operations can rotate pairing, revoke/delete a device and inspect bounded audit history.

## Reverse-proxy requirements

- HTTPS is mandatory for remote use.
- Do not log full `/v1/config/` or `/v1/bundle/` secret paths.
- Do not log `Authorization` headers.
- Device long polling needs an upstream timeout longer than the poll interval; roughly 35–60 seconds is a practical proxy setting.
- Configuration responses should remain `Cache-Control: no-store`.

## Data files

Protect Provisioning state as private service data. Typical files include:

```text
profiles.json
bundles.json
devices.json
administrator password state
```

Use restrictive permissions such as `0600` and keep backups with the same secrecy as the live data.

## Relationship to Relay/Landing tuning

Provisioning does not carry application payload and therefore the project recommendation **Landing=CUBIC / Relay=BBR** is unrelated to the Provisioning HTTP server itself. Apply those congestion-control roles to actual Landing and Relay hosts, not blindly to the control-plane server.

## Version matching

For normal production operation use a matched **v1.1.1 Client + v1.1.1 Landing + v1.1.1 Provisioning** suite. The control-plane formats retain migration compatibility, but matched versions reduce ambiguity during incident response.
