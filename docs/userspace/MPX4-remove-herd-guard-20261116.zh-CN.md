# MPX/4 v1.2.2 候选：完全移除 Herd Guard（2026-11-16）

**决策**：用户明确要求删除 Herd Guard，而不是调小阈值或设置成默认关闭。当前改动已写入隔离候选分支 `perf/v122-single-flow-faststart-20261116`；**生产 HKBN / Relay / 客户端没有修改、重启、部署或发布**。

## 已移除

- `weightedHerdGuardLocked()`、`recentActiveTXStreamsLocked()`，以及 64 开启 / 32 退出的累计连接数与近期 TX 压力状态机。
- `Session` 的 `weightedHerdGuard`、`txPendingStreams`、`txDemandSample*`、guard 进退计数；Stream 的 `lastTXQueuedAt`；状态快照中的 `active_tx_streams`、`weighted_herd_guard*` 字段。
- 当 Guard 触发时把 Weighted Flight 从 `weightedAdaptiveFlightBudget` 降级为 `weightedFlightBudget` 的分支，运行态始终走自适应预算。
- 因 Stream 数量达到 64 条而禁止首 DATA 有界优先的分支；Routed 和非 Routed 的 Weighted 首 DATA 都不再受全局 Stream 数量影响。
- 守卫专用的旧测试。将真正有意义的可用 Carrier、DRR 公平和大连接数测试分别更名、调整为不依赖守卫状态。
- 旧 `weightedFlightBudget` 从生产编译源码中移除。历史 A2 对照公式仅保留为 `weighted_test.go` 中的 `legacyWeightedFlightBudgetForTest`，从而不携带死生产代码。

## 仍然保留的独立安全机制

- **Weighted 自适应 Flight Budget**，根据路径基础 RTT、ACK/交付样本和 Socket Writer 状态调节，仍有 per-Carrier 上限、100ms 调节间隔。
- **Session 最大总在途 48MiB**；每 Carrier 普通 DATA 队列及额外最多两个优先 DATA 槽，控制帧仍优先。
- **首 DATA 共享 Token 和 ACK 额度回补**；8MiB 就绪 DATA 背压时，首包额度仍从 256KiB 收缩至 64KiB。此判断按实际队列压力，*不是* Herd Guard。
- **Route DRR 字节公平、Stream/Session WINDOW、Queue Admission 与精确唤醒**。

## 本机验证（六路模拟，非公网）

隔离源码目录：`.scratch/perf-20261116-no-herdguard/engine`。环境 Go 1.27.2、macOS arm64，模拟六路 94Mbps / 50ms RTT，真实 Routed TCP 后端。

- `go test ./... -count=1 -timeout 160s`：通过。
- `go vet ./...`：通过。
- 多项 WO-01、Weighted、Route DRR 和首 DATA 专项测试：通过。
- `go test -race` 的专项竞态检查：通过。
- Linux amd64 出口跨平台编译：通过；文件 `.scratch/perf-20261116-no-herdguard/artifacts/mpx-egress-no-herdguard-amd64`；SHA-256 `e9cf0824640dd07e91104428176728d21d2e5737e9a36d9a42e07b8762c62c01`。**未部署**。
- Go 源码扫描：无 `weightedHerdGuard`、`recentActiveTXStreams`、`herd_guard`、`txPendingStreams`、`lastTXQueuedAt` 引用；没有 `herd` 命名测试文件。

### 112 条空闲连接 + 下载

| 场景 | 原 v1.2.2 | 上一版 TX-active Guard 候选 | 完全删除 Guard |
|---|---:|---:|---:|
| 单流下载 | 530.2 Mbps | 530.5 Mbps | **531.6 Mbps** |
| 8 流下载 | 441.8 Mbps | 461.7 Mbps | **463.0 Mbps** |

以上数值为对应批次各自的中位数：新方案 3 轮，旧方案 5 轮；相近但不是同时同一轮交错重测，因此不适合把小差异解读为确定改善。

### 150 条真实同步大流

预建 150 条 Routed TCP（绕过后端 8 槽并发拨号的建连限制），同时发起 `150 × 4MiB = 600MiB`：

| 版本 | 3 轮 Mbps | 中位数 |
|---|---|---:|
| v1.2.2 原版 | 235.9、217.1、267.0 | 235.9 |
| 上一版有 TX-active Guard 的候选 | 227.6、369.5、404.8 | 369.5 |
| 完全删除 Guard | **317.3、203.1、353.5** | **317.3** |

三轮数据完整性全部通过。无 Guard 版本的高并发吞吐波动大，中位数低于上一版候选，不能证明删除不会带来真实高并发退化。当前已按用户决策删除，但**不应宣称高并发性能全面提升或真实公网单流已恢复 550Mbps**。

## 代码范围与下一步

- 核心源码修改：`weighted.go`、`firstdata.go`、`session.go`、`stream.go`。
- 测试更新：`wo01_b1_test.go`、`wo01_b1_bidirectional_test.go`、`wo01_a2_test.go`、`weighted_test.go`、`routed_firstdata_priority_test.go`、`routed_weighted_path_regression_test.go`、`wo01_weighted_work_conserving_test.go`、`routed_warm150_perf_test.go`、`single_flow_downlink_perf_test.go`。
- 独立性能日志：`.scratch/perf-20261116-no-herdguard/logs/`。
- 原有完整源码及 AB 基线：`.scratch/perf-20261116-herd-route/engine`（删除前候选）、`.scratch/perf-20261116-herd-route/base-engine`（1.2.2 原版模拟）。
- 之前关于“改用近期 TX Stream 的 Herd Guard”的方案仅为**历史实验**，不再适用于现行候选。

**Git 状态**：代码已通过 CodePier 的文件工具更新到本项目隔离 worktree；worktree 命令执行仍因 `TASKS_DISABLED` 被拒绝，本次未在该 worktree 创建 Git commit、推送或发布。不可绕过该权限执行本地 Git。

**上线前风险控制**：如后续做真实公网 A/B，应同时测试单流起速、8/64/150 并发流、重传、长尾、Flight/Window、CPU/RSS；保留原版可回滚二进制。
