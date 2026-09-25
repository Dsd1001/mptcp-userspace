# 文档索引

[English](README.md)

这里是 MPTCP Userspace 的公开文档入口。

## 新用户先看

- [快速开始](guides/QUICKSTART.zh-CN.md)：下载安装 macOS Client 与 Linux Landing。
- [从源码构建](guides/BUILDING.zh-CN.md)：工具链、Source-ID 与构建产物。
- [架构说明](guides/ARCHITECTURE.zh-CN.md)：Mac、Relay、Landing、Backend 之间的数据流。
- [部署与回滚](userspace/DEPLOYMENT.zh-CN.md)：托管 Landing 的升级、校验与 rollback。
- [故障排查](guides/TROUBLESHOOTING.zh-CN.md)：无速度、路径摸鱼、Weighted 不均匀、RTT 上升、版本不兼容等常见问题。

## 调度与协议

- [MPX/3 协议](userspace/PROTOCOL.md)：认证 hello、帧、方向性、Rev5 Weighted 容量字段。
- [调度策略](userspace/SCHEDULER-MODES.md)：Auto / Aggregate / Protect / Weighted 的详细行为。
- [MPX/3 Credit](userspace/MPX3-CREDIT.md)：会话/单流信用、WINDOW 与资源上限。
- [Adaptive Flow Control](userspace/ADAPTIVE-FLOW-CONTROL.md)
- [Delivery](userspace/DELIVERY.md)
- [Rev2 Shared Credit 历史设计](userspace/REV2-SHARED-CREDIT.md)
- [0.7.1 Credit Admission 历史设计](userspace/CREDIT-ADMISSION-0.7.1.md)

## 验证与 Release

- [验证边界](userspace/VALIDATION.md)
- [0.9.4 Release Notes](userspace/RELEASE.zh-CN.md)

Release 中的 ACCEPTANCE.md、TESTS.json、SCHEDULER-MODES.json、PROVENANCE.json 等是对应二进制的冻结验收证据，不在 main 重复维护副本。

v0.9.4 Tag 固定对应正式发布源码；Tag 之后 main 可以继续增加文档提交。
