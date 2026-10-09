# MPTCP Userspace v1.1.2

发布日期：2026-10-09

## 更新内容

**Queue-aware Admission**：发送方每个 Session 默认启用 32 MiB 的未调度 DATA 队列背压，大流在约 28 MiB 后开始等待，保护新 Stream 的首次 DATA。实现不修改 MPX/4 Stable Wire Protocol、Session 128 MiB Credit、单 Stream 16 MiB Window、可靠传输账本和跨 Carrier 重传规则。允许通过环境变量 MPX_QUEUE_ADMISSION_MIB=0 关闭新版 Admission，或设置 16/32/64 进行 A/B 对照。

状态新增 ready_data_bytes、queue_admission_limit_bytes、queue_admission_waiters 和 write_wait_reasons.queue_admission，区分 Session WINDOW Wait 与应用层队列等待；优化成效不能只看 Session Window Wait 次数。

**macOS Userspace-only**：彻底移除 Native / 内核 MPTCP 模式选择、原生 MPTCP socket 传输、系统聚合 sysctl 管理和相关授权弹窗。Mac GUI、Linux 和 Windows 客户端只接受 schema_version=3 与 mode=userspace_multipath。

旧 Native / tcp_forward 配置不自动转换；升级时原有偏好数据不删除，但不会自动启动旧模式。必须补齐 Userspace Relay 和 Transport Key 后才能启动。Provisioning 服务器若仍下发旧 Native Profile，也会被拒绝。

**跨平台套件**：macOS Universal MPTCP Desk、Windows GUI、Linux amd64/arm64 Client、Landing amd64/arm64 与 Provisioning 版本统一到 v1.1.2。固定的 MPTCPKeychainBroker v1 字节保持不变，不再重复要求钥匙串授权。

## 测试与限制

发送队列五组定向回归、Go engine/multipath/Landing 核心测试、race、静态审查、6 条 92 Mbps 限速 Carrier + 150 Stream 的离线 A/B 均需通过。离线 A/B 不构成真实 WAN 提速承诺；请比较 TCP 有效吞吐、P95/P99 延迟、queue_admission、session_window、内存 RSS 和 Carrier Flight Budget。

发布不会自动替换 HKBN 的运行中 Landing。升级服务端需独立操作，提前备份并考虑重启 Session 中断。推荐 Landing=CUBIC、Relay=BBR。

## 回退

对端不必同步更改协议。若需取消实验性队列软限，在该端服务环境设置 MPX_QUEUE_ADMISSION_MIB=0 并重启。Mac Native / 内核 MPTCP 模式不提供回退入口，需要使用 v1.1.1 或更早客户端，并自行管理旧配置兼容性。
