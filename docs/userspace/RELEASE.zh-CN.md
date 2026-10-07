# MPTCP Userspace 1.1.0 / MPX/4 Protocol Version 4 Stable

1.1.0 是一次 **MPX 数据面并发架构重构版本**。MPTCP Desk、Linux Client、Landing 与 Provisioning 统一使用 **1.1.0**。MPX/4 Wire Protocol Version 仍为 4，Capability Revision 仍为 8，协议源继续冻结到 MPX/4 `protocol-v4.0.0`，commit `44f587fd279ed2238b070dd68114c76822353f4d`。

本版本不通过修改 MPX/4 wire semantics 解决并发问题，而是重新组织 `mptcp-userspace` 内部的 Session / Stream / dispatcher 热路径，使 Session 级共享语义不再等价于高复杂度中央执行路径。

## 1.1.0 数据面重构

- DATA ready 调度从“每轮扫描 ready map、收集 Stream ID 并排序”改为 **持久化 per-Stream intrusive ready ring**。Stream 进入/退出 ready 状态时增量维护，正常派发热路径不再创建 ID slice 或执行 `sort.Slice`；
- dispatcher 单次 Session 临界区最多处理 **128 个 DATA frame**。如果仍有 ready work，会立即 self-kick 继续派发，避免一个调度周期长时间占用 Session 锁；
- dynamic bootstrap pending reserve 改为 **O(1) 增量计数**。活跃 bootstrap writer 在状态变化时更新 `bootstrapWriters`，`writeAllowance` / `writerTurn` 不再为每次判断重新扫描全部 writer；
- writer-turn scarcity 路径移除“外层 writer scan × 内层 reserve scan”的嵌套复杂度；保留真正资源不足时的 FIFO fairness，同时新增 scan-step telemetry；
- 大 application Write 增加 Session-lock quantum：一次连续 DATA commit 最多处理 **8 帧 / 256 KiB** 后主动让出 Session 锁，让 Carrier receipt、dispatcher 和其它 Stream 有机会进入共享账本；
- Stream receive storage 增加独立 `rxMu`。用户 `Read` 的实际 page → user buffer copy 会冻结 Stream 本地 receive pages 后释放 `Session.mu` 执行，再短暂返回 Session 账本完成 consumed credit / WINDOW / page accounting；不同 Stream 的用户数据复制不再必须互相串行；
- receive page reassembly 由 Stream-local `rxMu` 保护，Session 仍负责协议要求的 final-size、aggregate credit、Transmission/reliability 与全局资源 accounting；
- 保留 1.0.3 的 per-Stream targeted wake，不恢复 wake-all；
- 保留 1 GiB DATA pending、dynamic bootstrap reserve、128 MiB Session flow-control hard limit、32 KiB `MaxPayload`；
- Weighted 继续使用 1.0.5 恢复的 0.10.x bounded feedback RTT 公式，本版本不再调整 Weighted 控制环。

## 可观测性

1.1.0 新增 Session 数据面并发 telemetry：

- `session_lock_count`
- `session_lock_wait_ns`
- `session_lock_wait_max_ns`
- `session_lock_hold_ns`
- `session_lock_hold_max_ns`
- `dispatch_runs`
- `dispatch_frames`
- `dispatch_ns`
- `dispatch_max_ns`
- `writer_turn_scan_steps`
- `ready_streams`
- `bootstrap_writers`

这些字段用于直接判断多 Stream 压力下是 Session mutex、dispatcher 还是 writer-turn 扫描成为瓶颈，而不再只通过 MPX RTT 或吞吐曲线间接推断。

## 协议兼容性

1.1.0 **不修改 MPX/4 Core wire semantics**：

- Wire Protocol Version：4
- Capability Revision：8
- Protocol Release：`protocol-v4.0.0`
- Protocol Source：`44f587fd279ed2238b070dd68114c76822353f4d`
- `MaxPayload`：32 KiB
- Session flow-control hard limit：128 MiB
- 最大本地 active Carrier：8
- 最大 Stream：2048

Session-wide Transmission ID、Session Flow Control、duplicate/replay、cross-Carrier reinjection 与 retirement 语义全部保持不变。本次变化仅改变本地实现如何并发执行这些语义。

## 0.10.x / 1.0.x 功能保留

- parallel Bundle/Profile 故障隔离；
- Last Known Good cache-first 恢复；
- Profile 自动重连；
- Sparkle 2 内置更新；
- 可选远程设备管理；
- Provisioning / Mac 新界面；
- Keychain Broker / App 本体分离；
- UoT；
- MPX/4 Stable；
- 1.0.2 的 1 GiB DATA pending；
- 1.0.3 的 targeted wake；
- 1.0.5 的 legacy bounded-feedback Weighted 数据面。

## Broker 冻结

1.1.0 **不升级 Broker**。继续复用冻结的 `MPTCPKeychainBroker` v1：

`sha256=5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`

Mac 构建与 release gate 继续硬校验该值。

## 发布验证

按用户要求，本版本完成 correctness / concurrency 回归后直接发版，**不运行实际带宽、WAN、capacity 或 scheduler performance promotion 测试**。

发布前要求并记录：

- engine 全包 `go test ./... -count=1`；
- Landing / multipath 全量回归；
- Provisioning 全包测试；
- 完整 `go test -race ./multipath -count=1`；
- engine 与 Provisioning `go vet ./...`；
- frozen source / artifact provenance；
- Linux amd64/arm64 二进制重建校验；
- Mac App / engine frozen-source rebuild；
- DMG、代码签名、Sparkle EdDSA 与冻结 Broker 校验。

因此 1.1.0 声明 **correctness/build evidence passed**，但不把本版本宣称为新的吞吐、capacity 或物理 WAN 性能验收结果。
