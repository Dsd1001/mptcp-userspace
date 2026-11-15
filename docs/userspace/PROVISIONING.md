# MPTCP Tunnel provisioning and remote controller 1.2.0

`mpx-provision` provides the tunnel-only configuration API and opt-in device control. Use HTTPS, a strong administrator password and private storage. The default data file is `./provisioning-data/tunnels.json`; set `MPX_PROVISION_DATA` explicitly for production. A legacy profiles.json/bundles.json is not converted or overwritten. Keep it for rollback and create a new tunnel store.

Environment: `MPX_PROVISION_LISTEN` (default 127.0.0.1:8088), `MPX_PROVISION_PUBLIC_BASE_URL` (HTTPS except loopback), `MPX_PROVISION_ADMIN_USER`, `MPX_PROVISION_ADMIN_PASSWORD` or its existing private password-file option. The browser admin interface uses Basic authentication; terminate TLS at a trusted reverse proxy. Do not log complete secret API paths at that proxy.

## Tunnel configuration

Create a tunnel in /admin with name, optional URL alias, one shared transport group and multiple business inlets. Public `GET /v1/tunnel/[alias/]token` returns the established AES-GCM authenticated provisioning envelope, not plaintext JSON. Decrypted payload is schema 4, kind tunnel_profile, with identity/revision/background preference and a shared_mpx4_tunnel config. Complete URLs are bearer credentials. Redirects are rejected by clients. API-token rotation immediately invalidates the old URL.

Admin APIs `/admin/api/tunnels` and `/admin/api/tunnels/{id}` manage records; updates, deletions and token rotation require expected_revision. Writes use private temporary files, fsync and atomic replacement; memory is published only after durable success. Concurrent stale edits receive 409. Deleting a tunnel still assigned to a device is refused. Old public /v1/config and /v1/bundle endpoints, and old profile/bundle admin APIs, return 410.

## Devices

Enable remote management on each client locally, save the controller address and enter the one-time pairing code. Each device may be assigned one tunnel. Desired state, restart, sync and update use monotonic generations; old profile/bundle assignments are retained for administrative reassignment but produce a stopped control view with no provisioning URL. No arbitrary command execution is available.

The backend persists device pairing, audit history, observed status and desired configuration. A configuration edit alone does not hot-swap live business connections; request Sync to refresh the cache and Restart when an intentional connection interruption is acceptable. Device deletion/unpair revokes future control authentication. Unassigned devices must not run a cached local configuration in response to a remote start.

## Container

Build from the repository root, because the controller imports the shared configuration package:

```sh
docker build -f provisioning/Dockerfile --build-arg SOURCE_ID=YOUR_VERIFIED_SOURCE_ID -t mpx-provision:1.2.0 .
# Or use provisioning/docker-compose.example.yml after setting a strong
# MPX_PROVISION_ADMIN_PASSWORD and configuring the public HTTPS base URL.
```

The distroless runtime runs as nonroot and needs a writable private /data volume. Container builds without a verified Source-ID explicitly identify themselves as unfrozen-container-build. Building a container does not configure TLS or expose an administrator password safely by itself.
