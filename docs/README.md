# Documentation

[中文索引](README.zh-CN.md)

This directory contains the public documentation for MPTCP Userspace.

## Start here

- [Quick Start](guides/QUICKSTART.md) — install the released macOS/Linux client and Linux Landing.
- [Build from source](guides/BUILDING.md) — toolchain, reproducible release source and build outputs.
- [Architecture](guides/ARCHITECTURE.zh-CN.md) — component and data-flow overview.
- [Deployment / rollback](userspace/DEPLOYMENT.zh-CN.md) — managed Landing upgrades and rollback.
- [Troubleshooting](guides/TROUBLESHOOTING.zh-CN.md) — common runtime and compatibility problems.
- [Linux headless client](userspace/LINUX-CLIENT.md) — amd64/arm64 Userspace MPX/4 client CLI.
- [Managed client provisioning](userspace/PROVISIONING.md) — self-hosted configuration page and secret client API URLs.

## Protocol and scheduling

- [MPX/4 Draft 04 protocol profile](userspace/PROTOCOL.md) — handshake, Secure Records, Generation replacement, Streams, error scopes and scheduler contracts.
- [Scheduler modes](userspace/SCHEDULER-MODES.md) — Auto, Aggregate, Protect and Weighted.
- [MPX/3 credit](userspace/MPX3-CREDIT.md) — stream/session flow control and memory boundaries.
- [Adaptive flow control](userspace/ADAPTIVE-FLOW-CONTROL.md)
- [Delivery](userspace/DELIVERY.md)

## Validation and release

- [Validation](userspace/VALIDATION.md)
- [v0.9.8 release notes](userspace/RELEASE.zh-CN.md)

Published release evidence such as ACCEPTANCE.md, TESTS.json, SCHEDULER-MODES.json and PROVENANCE.json is attached to the GitHub Release rather than duplicated into the default branch.

The v0.9.8 tag identifies the current release source.
