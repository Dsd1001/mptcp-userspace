# Documentation

[中文索引](README.zh-CN.md)

This directory contains the public documentation for MPTCP Userspace.

## Start here

- [Quick Start](guides/QUICKSTART.md) — install the released macOS client and Linux Landing.
- [Build from source](guides/BUILDING.md) — toolchain, reproducible release source and build outputs.
- [Architecture](guides/ARCHITECTURE.zh-CN.md) — component and data-flow overview.
- [Deployment / rollback](userspace/DEPLOYMENT.zh-CN.md) — managed Landing upgrades and rollback.
- [Troubleshooting](guides/TROUBLESHOOTING.zh-CN.md) — common runtime and compatibility problems.

## Protocol and scheduling

- [MPX/3 protocol](userspace/PROTOCOL.md) — authenticated hello, records, directionality and Rev5 capacity fields.
- [Scheduler modes](userspace/SCHEDULER-MODES.md) — Auto, Aggregate, Protect and Weighted.
- [MPX/3 credit](userspace/MPX3-CREDIT.md) — stream/session flow control and memory boundaries.
- [Adaptive flow control](userspace/ADAPTIVE-FLOW-CONTROL.md)
- [Delivery](userspace/DELIVERY.md)

## Validation and release

- [Validation](userspace/VALIDATION.md)
- [v0.9.4 release notes](userspace/RELEASE.zh-CN.md)

Published release evidence such as ACCEPTANCE.md, TESTS.json, SCHEDULER-MODES.json and PROVENANCE.json is attached to the GitHub Release rather than duplicated into the default branch.

The v0.9.4 tag is the frozen release source. Documentation-only commits may appear on main after the tag.
