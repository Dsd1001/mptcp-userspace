# MPTCP Userspace 1.1.1 / MPX/4 Protocol Version 4 Stable

1.1.1 是一次 **发送侧 Flow Control 简化与多 Stream 并发修复版本**。MPTCP Desk、Linux Client、Landing 与 Provisioning 统一使用 **1.1.1**。MPX/4 Wire Protocol Version 仍为 4，Capability Revision 仍为 8，协议源继续冻结到 MPX/4 `protocol-v4.0.0`，commit `44f587fd279ed2238b070dd68114c76822353f4d`。

本版本针对 1.1.0 多 Stream 实测中仍出现的平台期，移除发送侧重复的本地 Growth Credit / writer-turn admission 层，让 MPX/4 对端正式发布的 Stream/Session WINDOW 成为发送许可的唯一流控真值。

## 1.1.1 发送侧信用模型

1.1.1 的 DATA 发送许可只受以下协议级条件约束：

- 对端 `STREAM_WINDOW`：`peerLimit - txNext`；
- 对端 `SESSION_WINDOW`：`peerLimit - txCommitted`。

同时继续保留本地资源硬保护：

- `MaxDataPending`；
- `MaxDataPendingBytes`；
- active bootstrap writer 的 pending-capacity reserve；
- Carrier flight/budget；
- receiver memory / page / Session resource limits。

以下 1.0.x/1.1.0 发送侧本地 admission 不再决定 DATA 是否可发：

- `SessionCreditLimit - txUsed` 二次发送硬闸；
- `sharedGrowthRoom(txUsed, txGrowth)`；
- Bootstrap/Growth 64/64 MiB 分账作为发送许可；
- `writerTurnLocked()` scarcity FIFO 串行化；
- per-Stream consumed 释放触发的 shared-growth writer wake；
- writer-turn handoff/wakeup 链。

因此，对端 aggregate `SESSION_WINDOW` 已经向前推进、但个别 Stream WINDOW/consumed replay 暂时滞后时，本地 `txUsed` 可以超过历史 128 MiB 诊断值，而不会错误地形成 Session 级 head-of-line blocking。

## 诊断账本与兼容 telemetry

`txUsed` / `txGrowth` 仍保留，用于观察 per-Stream consumed replay 相对 aggregate Session consumption 的滞后，但从 1.1.1 起它们是 **diagnostic mirrors**，不是发送 admission pool。

`credit_accounting` 更新为：

`rev5_peer_window_authoritative`

为了保持现有 status JSON / Desk / 监控兼容，以下 legacy 字段继续保留，但生产发送路径不再进入对应 wait：

- `bootstrap`
- `growth`
- `writer_turn`
- `writer_turn_waiters`
- `writer_turn_scan_steps`

正常 1.1.1 运行中，这些发送侧 legacy wait 应保持为 0；实际发送阻塞应主要归因于：

- `stream_window_or_open`
- `session_window`
- `pending_frames`
- `pending_bytes`

## 本地 pending 公平性

本版本没有删除 bootstrap pending reserve。

其语义仅是**本地队列容量保护**：当新活跃 Stream 仍需要首个 DATA frame 时，已经进入 post-bootstrap 的 bulk Stream 不应占满所有 pending frame/byte slot。

该 reserve 不属于 MPX/4 Flow Control，也不会再与 `txUsed/txGrowth` 绑定。

## 1.1.0 并发重构全部保留

1.1.1 保留 1.1.0 的数据面架构：

- per-Stream intrusive ready ring；
- dispatcher 单次最多 128 DATA frame 的有界 Session 临界区；
- O(1) dynamic bootstrap reserve accounting；
- 大 Write 每 8 frame / 256 KiB 主动让出 Session 锁；
- Stream-local `rxMu`；
- DATA payload store / overlap validation 脱离长 Session 临界区；
- 用户 Read 的 page → user buffer copy 脱离 Session 全局锁；
- 同一 Stream 跨 Carrier DATA 使用 `rxOpMu` 串行 publication；
- per-Stream targeted wake；
- Session mutex / dispatcher telemetry。

## 协议兼容性

1.1.1 **不修改 MPX/4 Core wire semantics**：

- Wire Protocol Version：4
- Capability Revision：8
- Protocol Release：`protocol-v4.0.0`
- Protocol Source：`44f587fd279ed2238b070dd68114c76822353f4d`
- `MaxPayload`：32 KiB
- Session flow-control hard limit：128 MiB
- 最大本地 active Carrier：8
- 最大 Stream：2048

Session-wide Transmission ID、Session/Stream Flow Control、duplicate/replay、cross-Carrier reinjection 与 retirement 语义全部保持不变。

特别地，**128 MiB Session WINDOW 没有被取消或扩大**。1.1.1 删除的是发送端额外叠加的一套本地 128 MiB `txUsed` admission，而不是协议定义的 Session Flow Control。

## 其它功能保留

- Weighted 继续使用 1.0.5 恢复的 bounded-feedback RTT 公式；
- 1 GiB DATA pending；
- UoT；
- parallel Bundle/Profile 故障隔离；
- Last Known Good；
- Profile 自动重连；
- Sparkle 2；
- 远程设备管理；
- Keychain Broker / App 本体分离；
- Provisioning / Mac 现有界面与功能。

## Broker 冻结

1.1.1 **不升级 Broker**。继续复用冻结的 `MPTCPKeychainBroker` v1：

`sha256=5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`

## 发布验证

按用户要求，本版本完成 correctness / concurrency 回归后直接发版，**不运行实际带宽、WAN、capacity、high-BDP 或 scheduler performance promotion 测试**。

发布前要求并记录：

- engine 全包 `go test ./... -count=1`；
- Landing / multipath 全量回归；
- Provisioning 全包测试；
- 完整 `go test -race ./multipath -count=1`；
- engine 与 Provisioning `go vet ./...`；
- peer WINDOW authoritative 专项回归：
  - 本地 `txUsed/txGrowth` 超过历史发送上限时，peer WINDOW 有 room 仍必须放行；
  - peer `SESSION_WINDOW` 耗尽时仍必须硬阻塞；
  - pending frame/byte reserve 仍独立生效；
- frozen source / artifact provenance；
- Linux amd64/arm64 二进制重建校验；
- Mac App / engine frozen-source rebuild；
- DMG、代码签名、Sparkle EdDSA 与冻结 Broker 校验。

因此 1.1.1 声明 **correctness/build evidence passed**，但不宣称新的吞吐、capacity 或物理 WAN 性能验收结果。

## 推荐生产网络基线

v1.1.1 的协议与二进制不会自动修改 Linux congestion control。当前项目推荐把生产测试基线固定为：

- **Landing：CUBIC**；
- **Relay：BBR**，优先配合 `fq`。

这是部署/性能基线，不是 MPX/4 wire requirement，也不会影响 Native UDP 的 TCP 拥塞控制。需要比较其它算法时，应以这组配置做单变量 A/B，并同时观察 loaded RTT、queue/outstanding、retransmit 与 CPU，而不是只比较峰值吞吐。

完整配置见 `docs/guides/NETWORK-TUNING.zh-CN.md`。
