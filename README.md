# MPTCP Tunnel 1.2.2

One shared MPX/4 Session, multiple business inlets, and up to eight ordinary TCP Carriers. macOS, Windows and Linux share the Go engine. Every business TCP connection uses an independent MPX Stream; routed UDP-over-TCP associations share the same Session. The common tunnel egress connects allowlisted ordinary TCP/UDP backends. There is no nested MPX or additional smux layer.

[中文部署与设计说明](README.zh-CN.md)

## Breaking configuration change

v1.2 is tunnel-only. Independent v1.x profiles, multi-Session bundles, fixed-backend product mode and native UDP Relay paths are not launched or silently converted. Core MPX/4 Stable framing remains unchanged, but both product endpoints must support routed services.

Client: `schema_version=1`, `kind=shared_mpx4_tunnel`. Egress: `schema_version=2`, `kind=mpx4_tunnel_egress`. Managed provisioning: `schema_version=4`, `kind=tunnel_profile`.

Use `examples/shared-tunnel/client.example.json` and `egress.example.json`; replace placeholders and use private file permissions. All Relay paths must converge on the same Session-owning egress. Backend addresses refer to business service ports, not legacy MPX listeners.

```sh
chmod 600 client.json egress.json
./mptcp-client validate < client.json
./mptcp-landing server --config egress.json
./mptcp-client run < client.json
```

The GUIs support one Relay group and multiple inlets, OS-protected credentials, background recovery and opt-in remote device management. `mpx-provision` distributes authenticated encrypted configurations and manages assigned tunnel devices; it exposes no remote shell.

## Isolation and limitations

A work-conserving byte-weighted route ring precedes per-Stream fairness and the existing Carrier scheduler. Actual receive occupancy is bounded per route and globally, with protected headroom. Advertised windows are never retracted. Local resource exhaustion resets the affected Stream, not the entire Session. Idle advertised entitlements do not permanently reserve memory.

Session limits remain 2048 Streams and 128 MiB receive credit/physical accounting. The common egress is a shared capacity and failure boundary. MPX protects Client-to-egress traffic; backend segments rely on existing application encryption or a trusted network. Existing business connections and stale UDP datagrams are not replayed after Session replacement.

A six-path tunnel can aggregate a single Stream, but real throughput depends on independent path capacity, shared bottlenecks, endpoint CPU and the egress-to-backend segment. Release artifacts must share one canonical Source-ID. Acceptance logs distinguish loopback, shaped laboratory, native platform and WAN results; historical reports are not current release evidence. See the release `TESTS.json`, `PROVENANCE.json` and `ACCEPTANCE.md`.
