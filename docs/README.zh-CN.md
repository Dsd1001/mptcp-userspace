# MPTCP Userspace 文档 — v1.1.1

本目录面向当前 **MPTCP Userspace v1.1.1 / MPX/4 Protocol Version 4 Stable**。

## 推荐阅读顺序

1. [快速开始](guides/QUICKSTART.zh-CN.md)
2. [网络与拥塞控制调优：Landing CUBIC / Relay BBR](guides/NETWORK-TUNING.zh-CN.md)
3. [系统架构](guides/ARCHITECTURE.zh-CN.md)
4. [部署、升级与回滚](userspace/DEPLOYMENT.zh-CN.md)
5. [故障排查](guides/TROUBLESHOOTING.zh-CN.md)

## 组件与运维

- [Linux Client](userspace/LINUX-CLIENT.md)
- [Provisioning / Managed Device](userspace/PROVISIONING.md)
- [构建说明](guides/BUILDING.zh-CN.md)
- [English build guide](guides/BUILDING.md)

## 协议、调度与验证

- [MPX/4 实现说明](userspace/PROTOCOL.md)
- [Scheduler 模式](userspace/SCHEDULER-MODES.md)
- [v1.1.1 Flow Control](userspace/ADAPTIVE-FLOW-CONTROL.md)
- [发布交付](userspace/DELIVERY.md)
- [验证边界](userspace/VALIDATION.md)
- [v1.1.1 Release Notes](userspace/RELEASE.zh-CN.md)

## 历史文档

下列文档只用于实现考古，不再代表 v1.1.1 当前行为：

- `CREDIT-ADMISSION-0.7.1.md`
- `MPX3-CREDIT.md`
- `REV2-SHARED-CREDIT.md`
- `RELEASE-0.10.1.zh-CN.md`

历史文档与当前源码/当前文档冲突时，以 v1.1.1 源码与本索引中的当前文档为准。

## 生产网络基线

当前架构推荐先固定：**Landing 使用 CUBIC，Relay 使用 BBR**。这是 Linux 主机调优建议，不属于 MPX/4 wire requirement；Native UDP 也不受 TCP congestion control 影响。
