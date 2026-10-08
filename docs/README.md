# MPTCP Userspace documentation — v1.1.1

This directory documents the current **MPTCP Userspace v1.1.1 / MPX/4 Protocol Version 4 Stable** implementation.

## Start here

1. [Quick Start](guides/QUICKSTART.md)
2. [Network tuning — Landing CUBIC / Relay BBR](guides/NETWORK-TUNING.md)
3. [Architecture](guides/ARCHITECTURE.zh-CN.md)
4. [Deployment / upgrade / rollback](userspace/DEPLOYMENT.zh-CN.md)
5. [Troubleshooting](guides/TROUBLESHOOTING.zh-CN.md)

## Components and operation

- [Linux Client](userspace/LINUX-CLIENT.md)
- [Provisioning and managed devices](userspace/PROVISIONING.md)
- [Windows MPTCP Desk](../windows/README.zh-CN.md)
- [macOS build guide](guides/BUILDING.md)
- [中文构建说明](guides/BUILDING.zh-CN.md)

## Protocol and scheduling

- [MPX/4 implementation profile](userspace/PROTOCOL.md)
- [Scheduler modes](userspace/SCHEDULER-MODES.md)
- [v1.1.1 flow-control model](userspace/ADAPTIVE-FLOW-CONTROL.md)
- [Delivery and release artifacts](userspace/DELIVERY.md)
- [Validation scope](userspace/VALIDATION.md)
- [v1.1.1 release notes](userspace/RELEASE.zh-CN.md)

## Historical documents

The following files are retained for implementation archaeology only. They do **not** describe the current v1.1.1 behavior:

- `CREDIT-ADMISSION-0.7.1.md`
- `MPX3-CREDIT.md`
- `REV2-SHARED-CREDIT.md`
- `RELEASE-0.10.1.zh-CN.md`

When a historical document conflicts with current source or the current documents above, v1.1.1 source and current documents win.

## Production TCP baseline

For the current architecture, use **CUBIC on Landing** and **BBR on Relay** as the first production baseline. This is host tuning, not an MPX/4 protocol requirement. Native UDP is unaffected by TCP congestion-control selection.
