# MPX/4 v1.2.2 Routed Weighted：真实 TX Herd Guard 与 Route-aware 首 DATA 优化验收

> **历史实验，已被后续决策取代。** 2026-11-16 用户要求从候选版本中彻底删除 Herd Guard；当前候选已移除连接数/活跃 TX 触发逻辑、保守 Flight 分支和首 DATA 禁用条件。本文件保留的是删除前的实验结论，不代表当前代码。参见 `MPX4-remove-herd-guard-20261116.zh-CN.md`。

日期：2026-11-16  
候选分支：`perf/v122-single-flow-faststart-20261116`  
基线：`v1.2.2`，沿用上轮的单 Stream 遥测、Queue Admission 精准唤醒修复和默认关闭的 TCP 后端缓冲区实验参数。

## 范围与目标

历史 1.1.6 的 WO-01 A1/A2/B1 优化包含有界 Flight、Weighted DATA 有效路径选择与首 DATA 专用快速通道；64/32 的 Herd Guard 是为真实大批同步发流设计。1.2.2 合并为一个共享 Session 之后，已有 112 条连接、48 条超 1 分钟无数据活动的 HKBN 快照；旧 `weightedHerdGuardLocked` 仅检查 `len(s.streams)`，会让空闲连接长期触发保护。旧 `dispatchLocked` 对 Routed 刻意跳过所有 first-DATA 快道。

目标是修正这两处而不破坏协议/资源限制、Route DRR 公平、原有非 Routed 行为或 150 真正同步大流保护。不修改现有 HKBN 生产进程或 iPhone/Mac 客户端。

## 已实现代码

- `macos/engine/multipath/weighted.go`：对 **共享 Routed** Weighted 使用最近 TX 活动与可靠待发送 DATA 计数判定 Herd Guard；旧非 Routed 仍按 Session Stream 数量判定。64 进入、32 以下退出的滞后阈值保留。实际待发送 Stream 数达到 64 时立即保护；ACK 清空后有 750ms 活动宽限；为避免每帧扫描大量 Stream，稀疏活动估计最多每 100ms 重采样一次。
- `session.go` / `stream.go`：在 DATA 新增/结算处维护 O(1) `txPendingStreams` 计数和每条 Stream 的最近 DATA 提交时间。状态增加 `active_tx_streams`、`weighted_herd_guard`、`weighted_herd_guard_basis`、进入/退出次数，均无业务正文。原有上轮 `active_send_streams` 明细保留。
- `firstdata.go` / `route_scheduler.go` / `flowcontrol.go`：每 Route 有独立 `firstReady` 链表。优先路径在 **DRR 已选择 Route 后**、且第一个 DATA 全额满足 Route 现有字节 deficit 时才执行。继承原先最多两帧 Carrier 优先槽、Session token/ACK 回补、Writer 公平退路、控制帧优先及 Herd Guard 安全限制，不给予额外 Route 发送机会；Session 停止时同步清空各 Route 的 `firstReady`，防止残留引用。
- `routed_herdguard_regression_test.go`：旧测试改为验证“64/97 条闲置身份不能开启 Routed Guard”，以及仍选得出可用 Carrier、Cost Deferral 保持 0。旧非 Routed Herd 测试不修改。
- `routed_active_herd_priority_test.go`：新增 112 空闲 + 1 活跃的判定、真正 150 同步 DATA 的进入退出、跨 Route DRR 字节公平、满普通 Carrier 队列的有界优先、取消/ACK 后链表和 token 一致性测试。
- `single_flow_downlink_perf_test.go`：可选六路成形回环端到端测试，能预热 112 条真正 Routed TCP 空闲连接。只在 `MPX_DIAG_DOWNLINK=1` 时执行。
- `routed_warm150_perf_test.go`：可选测试，先依次握手 150 条真实后端 Routed TCP，再同时释放闸门发送 150×4MiB；只在 `MPX_DIAG_WARM150=1` 时执行。这避免误把后端 8 路并发拨号与 5 秒回复超时误判为数据面拥塞。

## 完成的测试

工具链 Go 1.27.2，macOS arm64；独立源码目录 `mptcp/.scratch/perf-20261116-herd-route/engine`，与 CodePier worktree 最终复制的 10 个变更文件逐文件 SHA-256 完全一致。

- `go test ./... -count=1 -timeout=160s`：**PASS**（包括最终的 Session 首包队列清理改动）。
- `go vet ./...`：**PASS**。
- 目标单元测试加 `go test -race`：**PASS**。
- Linux amd64 无 CGO 跨平台出口编译：**PASS**；候选可执行文件 `.scratch/perf-20261116-herd-route/artifacts/mpx-egress-herd-route-amd64`，SHA-256 `f4e89d109e2f95b04317e21641e233625286fbb25a0268b1c6ae92381fd7e84f`（已随最终清理改动重新编译）。**未部署**。

### A/B：六路 94Mbps、50ms RTT 模拟；Weighted；真实 Routed 后端

5 组交错 112 条已确认建连但空闲、仅 1 / 8 条下载流（单次总64MiB）：

| 场景 | 原版吞吐中位数 | 候选中位数 | 变化 |
|---|---:|---:|---|
| 112 空闲 + 1 单流 | 530.2 Mbps | 530.5 Mbps | 基本无变化 |
| 112 空闲 + 8 并发流 | 441.8 Mbps | 461.7 Mbps | +4.5% |

3 组交错不带额外空闲连接：

| 场景 | 原版吞吐中位数 | 候选中位数 | 变化 |
|---|---:|---:|---|
| 无额外空闲 + 1 单流 | 529.2 Mbps | 528.9 Mbps | 基本无变化 |
| 无额外空闲 + 8 并发流 | 462.1 Mbps | 459.2 Mbps | -0.6% |

原版 112 空闲后仍 `herd_guard=true`、`first_data_priority_frames=0`；候选为 `herd_guard=false`、首 DATA 有提升，说明**核心触发误判已被消除**。

### A/B：真正同步 150 流（预先握手，再同时发送）

每次 150×4MiB=600MiB，3 轮交错测试，两组全部传输完整、真正同步期间 Herd Guard 都会打开：

| 版本 | 三轮 Mbps | 中位数 |
|---|---|---:|
| 原版 | 235.9、217.1、267.0 | 235.9 |
| 候选 | 227.6、369.5、404.8 | 369.5 |

**谨慎解释**：原版和候选在 150 流下均出现显著波动，样本数量只有三组，不能把候选 150 流吞吐中位数差距当成稳定提升或完整压力回归证明。候选真实同步下载期间 Guard 确实开启，且无完整性错误。原版首次 150 并发 OpenRoutedTCP 夹具曾因后端 Route 并行拨号槽上限而超时，已使用预热夹具分离建连与数据压力。

## 仍未完成

1. **真实公网单流 550+ 恢复尚未证实**：112 空闲的单流模拟几乎不变，说明 Guard 误判并非 300Mbps 症状的完整根因。
2. 150 流性能仍需更多轮次以及 P95/P99、CPU/RSS、丢包、TCP Writer Stall 的联合验收；本次重复测试侧重吞吐、数据完整性和 Guard 开关。
3. 下一阶段选择隔离的共享出口灰度，通过新遥测关联每秒 `stream_send_room_bytes`、`active_tx_streams`、Carrier Flight、Route 队列及 iCable 后端 TCP 接收窗口，进行同一 Speedtest 节点单流/多流实测，不可直接把本机回环数据作为公网结论。
4. **本次未在 CodePier worktree 提交 Git commit**：该 worktree 的本机命令执行标记为 `TASKS_DISABLED`。全部源码修改已由 CodePier 文件工具存入分支对应隔离目录。项目原始工作树和生产机器没有被更改。

## 关联文件

- 上轮单 Stream 诊断：`docs/userspace/MPX4-1.2.2-single-stream-diagnostics-20261116.zh-CN.md`
- 历史调优复盘：`docs/userspace/MPX4-historical-tuning-retrospective-20261116.zh-CN.md`
- 测试日志：`.scratch/perf-20261116-herd-route/logs/`
- 实测原版/候选两套独立源码：`.scratch/perf-20261116-herd-route/base-engine`、`.scratch/perf-20261116-herd-route/engine`

**状态：独立代码和本地测试通过、尚未公网验收或发布，不应把本报告理解为已在生产修复了实际 Speedtest 低速问题。**
