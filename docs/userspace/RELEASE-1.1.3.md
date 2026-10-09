# MPTCP Userspace v1.1.3 — Userspace PR #2 Integration

发布日期：2026-10-09。此版本从 v1.1.2 的 `main` 创建独立 `release/v1.1.3-pr2` 分支，在该分支合并 [userspace PR #2](https://github.com/Dsd1001/mptcp-userspace/pull/2)，`main` 暂不变更。

## 变更内容

- **Carrier 批处理优化**：复用批处理 Frame 列表、Secure Record 拼接缓冲及帧编码切片，减少分配和 GC 压力。
- **Pending DATA 快速判断**：使用 Stream 范围的 pending 计数，避免 FIN/关闭路径反复扫描 Session pending 映射。
- **协商限制**：发送侧尊重对端 MAX_FRAME_PAYLOAD、MAX_RECORD_SIZE、MAX_STREAMS；接收侧允许仅含 PADDING/可忽略扩展的 Record。
- **Provisioning**：设备状态变更及审计数据持久化失败时回滚内存状态，增强 Public Base URL 检验。
- **v1.1.3 集成修复**：写出 Secure Record 后清理连接复用的明文缓冲区，并增加专项回归测试。

## 协议与兼容性

- MPX/4 Protocol Version 4 Stable 不变，协议源码仍固定 `44f587fd279ed2238b070dd68114c76822353f4d`。
- 保留 v1.1.2 的 Queue-aware Admission、Userspace-only 模式，以及冻结的 MPTCPKeychainBroker v1；没有 Native/内核 MPTCP fallback。
- 建议 Landing 使用 CUBIC，Relay 使用 BBR。
- 本 Release 不会自动部署到现有 HKBN Landing/Relay；升级服务端前请做好回退准备。

## 测试提示

本版本用于对照 v1.1.2 的真实链路性能。重点比较多 Stream 总吞吐、单 Stream、P95/P99 延迟、Carrier 重传与 GC/CPU，以及 Session Window Wait 和 Queue Admission Wait。微基准的分配优化并不等于实际 WAN 带宽提升。

## 回退

如果需要回退，重新安装 v1.1.2 的 Client/Landing/Provisioning 对应资产；留意 Session 会重建，不能无中断热切换。
