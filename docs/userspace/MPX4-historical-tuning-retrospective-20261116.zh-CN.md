# MPTCP Userspace 历史性能调优复盘与 1.2.2 回归假设（2026-11-16）

> **更新说明：** 此文记录的是当时的调优假设。随后按用户决定，已在候选代码中完全移除 Herd Guard（而不是只修改阈值）；本文关于保留该功能的建议不再适用。现行候选及验收见 `MPX4-remove-herd-guard-20261116.zh-CN.md`。

> 依据仓库 tag/git commit、历史发布文档、现存模拟 A/B 及 HKBN 只读运行快照。不是此前全部聊天记录的替代，也不把模拟数据当公网指标。本报告不修改生产行为。

## 一、过往验证过的重要经验

| 阶段 | 当时问题与解决办法 | 对共享架构的启示 |
| --- | --- | --- |
| 0.7.1 | 稀疏启动流量被容量估计器误当成物理降速：旧路径分不到足够 DATA、失去反馈机会。仅预算真正被填满时，低吞吐样本才可下调容量先验。 | 保留“应用未充分提供负载 != 线路慢”的保护，观察单流早期每条 Carrier 的实际发送资格、采样有效性。 |
| 1.0.0 RC3–RC8 | 路径起速、短突发、发送信用反馈反复优化；RC7 最终采用乐观的 Stream WINDOW 授权，而非为每个空闲 Stream 预留 16 MiB 实际内存。 | 不应重新引入“按已连接 Stream 数量平均分配固定信用”的旧门槛。 |
| 1.1.1 | 删除把 txUsed/txGrowth 当成第二套发送信用硬门的做法；以真实对端 Stream/Session WINDOW 为准，分离接收副本、精确唤醒等待者。 | Stream WINDOW、Session WINDOW、路由队列和 Carrier flight 必须分别观察，不可混为一个瓶颈。 |
| 1.1.2–1.1.4 | 32 MiB Queue-aware Admission 保护新流，按下一帧实际大小唤醒，新增 path_selection 分类及持续采样。 | 共享路由的新二级配额也需要进入准入唤醒条件；2026-11-16 候选分支已修复这一处。 |
| 1.1.5 | 阻塞的 Carrier Socket Writer 不应拖住可经其他路径发送的 WINDOW、OPEN_OK、可靠控制帧；模拟故障时短流 TTFB 大幅改善。 | 继续把控制面延迟与 DATA 吞吐分开分析；保证 ACK 原路径归属及异常降级。 |
| 1.1.6 WO-01 | A1 合格路径之间工作保守选路、排除阻塞的 Socket Writer；A2 有界自适应 Weighted Flight；B1 Session token+ACK 回补的首 DATA 快道；≥64 Stream Herd Guard 防同步突发。异构 12 流模拟曾由约 390.7 到 542.6 Mbps；150 同步流新候选反而约低 20%。 | 不能粗暴取消保护。应把“真实同步发流”与“许多闲置 Stream”区别开来，恢复有边界的快速起速。 |

历史资料：`docs/userspace/RELEASE-1.1.6.zh-CN.md`、`RELEASE-1.1.5.zh-CN.md`、`RELEASE-1.1.4.md`、`CREDIT-ADMISSION-0.7.1.md`、`ADAPTIVE-FLOW-CONTROL.md`（按对应 tag 阅读）；测试 `wo01_a1_test.go`、`wo01_a2_test.go`、`wo01_b1_test.go`、`wo01_herd_guard_test.go`、`weighted_startup_test.go` 等。

## 二、当前首次发现的架构错配：Herd Guard 按已存在 Stream 数量而非 TX 竞争

在 HKBN 真正的 1.2.2 共享出口 `/run/mpx-tunnel-egress/status.json`，2026-11-16 香港时间约 02:23：
- `configured_scheduler_mode=weighted`，六条 Carrier，均有 `94 Mbps` 容量提示；所以升级时把生产模式从 Weighted 改成 Auto 不是本次证据支持的解释。
- 共享 Session 约 `112` 条活跃生命周期 Stream，其中 `resources.idle_streams=91` **是接收侧闲置分类，不等于双向无流量**；`data_idle_over_1m=48` 表示至少48条连接已一分钟无数据活动。
- `active_demand_streams=8`、`active_bulk_streams=6` **都是接收侧需求口径，不能当发送活跃连接数**。
- `first_data_priority_frames=0`。
- 同台保留的旧 1.1.6 Landing，三个有流量 Session 分别约 `18/5/16` 条 Stream，均小于 Herd Guard 的64条阈值；历史 `first_data_priority_frames` 累计大于0。

源码 `weighted.go:weightedHerdGuardLocked` 使用 `len(s.streams)>=64` 开启，直到 `len(s.streams)<32` 才解除；并不观察 TX 侧同步发流数量。共享架构把三个入口全部合在一个 Session，尤其包括预开、后台和空闲 Stream，容易长期处于保守状态。

在 `session.go:aggregatePathLocked` 中，Herd Guard 生效时改用保守的 `weightedFlightBudget`，而非正常 `weightedAdaptiveFlightBudget`。1.2.2 虽修复了 Routed Weighted 的 25ms Cost Deferral，但**没有恢复被 Herd Guard 禁用的自适应 Flight**。

在 `flowcontrol.go:dispatchLocked` 中，存在 `routeGroups` 时根本不进入旧的首 DATA 快道；`firstdata.go` 自身也会在 Herd Guard 状态跳过快道。因此 1.1.6 的 B1 有界首包优先并未延续到 Routed 数据分发。恢复应先满足跨 Route DRR 字节公平；不能重建“全局抢占所有 Route”的快道。

**归因等级**：代码控制路径/运行时阈值误分类已确认；是否足以解释真实 Speedtest 的 550→300 Mbps 还未做同一链路 A/B。当前状态时间也与此前四轮测速时间不同，不应反推测量当时一定有112条 Stream。

## 三、分级借鉴方案

P0：给 Weighted 发送方向单独定义“近期活跃 TX Stream / 同步 Bulk 压力”指标，不能简单复用接收侧 `active_demand_streams`。TX 活动判据可以结合写入尝试、pending DATA、就绪帧和近期确实提交的数据；注意避免在每个 Carrier 选路热路径扫描全部2048条 Stream。保留 64/32 双阈值、滞后/冷却和真实150同步流保护；把 `herd_guard_active`、`herd_guard_reason`、`recent_active_tx_streams` 放入 Session 快照。先用只读数据验证，再考虑调整规则。

P1：Routed 按 Route/Stream DRR 结果允许受控首包优先，沿用历史 token/ACK 回补、单 Carrier 不超过2额外槽、控制帧优先及 48MiB Session flight 上限。此项主要改善 TTFB/短流，不应宣称自动修好64MiB大下载全程速度。

P1：核实额外 HKBN→iCable TCP 后段与 `Bridge` 同步 `Read→Stream.Write` 是否把 MPX 发送背压传导成内核接收窗口变小/零窗口。前一次抓包已观察到初始约48KiB至1.5MiB的窗口成长与少量晚期零窗口。不要直接把 `SO_RCVBUF` 固定512KiB设为默认：隔离模拟并未获益，且可能关闭 Linux autotune。

P2：保留 RC7 乐观 Stream WINDOW 与真实 Session Credit 作为权威，以及 32MiB Queue Admission、已完成的分 Route 唤醒修复；旧的“单纯扩大窗口/队列”、取消所有 Cost Deferral，在2026-11-16隔离 A/B均没有稳定单流收益。

## 四、建议复现实验矩阵与闸门

完全相同六条路径、真正 Routed 后端、同一 Service Source-ID：
- 连接数量 `1 / 16 / 63 / 64 / 112`，其中 `64/112` 必须单独做“多数空闲、单条下载”的组，保证业务并发没有混入。
- 真实同步 `12 / 64 / 150` 条大流以及长短流混合，防止放宽保护后150条同步流再度退化。
- 对照 `v1.2.2 原版`、`只读遥测版`、`新 TX-active Herd Guard`、`Herd Guard + Route-aware 首包`；每组至少5轮交错，记录起速前1/3/10秒、稳态带宽、P95、重传、CPU/RSS、各 Carrier Flight/Outstanding、窗口和后台后段窗口。
- 若只有改善“超过64条已连接但基本无流量”且真实150条同步流不退化，才考虑逐步灰度；公网与仿真闸门分开，先备份回滚，绝不覆盖生产正在运行的出口。

## 五、明确不应误读的事实

目前实际共享出口已经是 Weighted 94Mbps×6；不能再以改Auto→Weighted作为生产提速建议。历史 WO-01 542.6Mbps 与现在用户公网422–436Mbps来自不同测试条件。历史 v1.1.6 的150同步流失败也不是“已经解决”的证据。旧0.x Credit文档不能当成现行MPX/4协议配置。源代码结构变化是已证事实，单独的性能根因仍需要新 A/B。
