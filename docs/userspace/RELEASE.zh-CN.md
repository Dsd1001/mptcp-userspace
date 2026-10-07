# MPTCP Userspace 1.0.3 / MPX/4 Protocol Version 4 Stable

1.0.3 是基于 1.0.2 的兼容补丁版本：MPTCP Desk、Linux Client、Landing 与 Provisioning 统一使用 **1.0.3**。Wire Protocol Version 仍为 4，协议源继续冻结到 MPX/4 `protocol-v4.0.0`，commit `44f587fd279ed2238b070dd68114c76822353f4d`。

## 1.0.3 变化

- 将高并发发送侧的 flow-control 唤醒从 Session 全局广播改为**按 Stream / 按实际释放额度定向唤醒**，消除 1.0.2 在 128 MiB Session credit 满载后的第二层 thundering herd；
- Stream WINDOW 只直接唤醒对应 Stream；shared/session credit 每释放一段额度，按 `MaxPayload=32 KiB` 折算 permit 数量，仅放行能够实际消费新增 credit 的少量 writer；
- `writer_turn` 改为 FIFO 定向传棒：writer 完成一个 turn 后移到队尾并通知下一个 eligible writer，不再让数十个 writer 同时争抢 Session mutex；
- OPEN / OPEN_OK / server accept 改为每 Stream 独立 `openWake`，收到 DATA 改为每 Stream 独立 `readWake`；单 Stream deadline 与本地连接计数变化也不再触发 Session 级广播；
- 普通 DATA ACK 继续只释放 pending storage；高频 DATA、ACK、WINDOW、OPEN_OK、Read 路径均不再调用全局 `wakeLocked()`，全局 wake 仅保留给 Session/Carrier/FIN/RESET/终止等真正的全局状态变化；
- Resource telemetry 新增 `credit_waiters`，可与 `pending_waiters`、`writer_turn_waiters` 一起区分 storage、flow-control credit 与公平调度等待；
- 继承 1.0.2 的 **1 GiB DATA pending pool + 动态 bootstrap reserve + pending permit**；`MaxPayload` 仍为 32 KiB，Session flow-control hard limit 仍为 128 MiB，Landing `MemoryMax` 仍为 2 GiB；
- 继承 1.0.1 的 192 KiB OPEN bootstrap 与 Weighted `minRTT` flight-budget 修复；Keychain Broker 继续冻结不变。

本版本通过当前源码的 Go 全包测试、`go vet`、定向唤醒/OPEN/flow-control 回归以及完整 multipath race；未重新声明新的 WAN、capacity 或物理 App/Surge 性能 acceptance。

## 协议更新

- 加入 critical `MAX_CARRIERS`，并把 Carrier ID 扩展为完整非零 MPX VarInt 空间；
- CREATE/JOIN 固化 Session-scoped limits，JOIN 改值按 SESSION_CONFLICT 处理；
- 加入 VERSION_NEGOTIATION 与 HANDSHAKE_REJECT；
- 正式支持 DORMANT Session 保留与 replacement recovery；
- 加入 TRANSMISSION_RETIRE、连续 Settled Through 和 confirmation replay retention；
- 收紧 Transmission confirmation class、credit reordering、terminal/final-size、recovery 与 error-scope 语义；
- 源码内置 Stable 的 20 个 Core JSON vectors；
- 不提供同端口 Draft 04 静默 fallback。

## Scheduler 语义

MPX/4 Stable Core 不再协商 Scheduler。Auto / Aggregate / Protect / Weighted 保留为 MPTCP Userspace 的本地策略。

Weighted 可使用已发布的可选扩展 `RECEIVE_CAPACITY_HINT (0x40)` 传递 receive-side 容量估计。该 Hint 不参与 Relay、不代表预留带宽，也不是 flow-control credit。

Landing 增加独立本地 scheduler policy，默认 Auto。

## 0.10.x 功能全部保留

- 0.10.1 parallel Bundle/Profile 故障隔离；
- 0.10.4 Last Known Good 缓存与 cache-first 恢复；
- 0.10.5 每 Profile 自动重连：1s → 2s → 5s → 10s → 30s，之后每 30s；
- 0.10.6 Sparkle 2 内置更新与可选远程设备管理；
- 0.10.7 Mac 与 Provisioning 新界面；
- 0.10.9/0.10.10 稳定本地代码签名与连续性检查；
- 0.10.11/0.10.12 Keychain Broker / App 本体分离。

## Broker 冻结

1.0.3 **不升级 Broker**。继续复用 0.10.12 / 1.0.0 / 1.0.1 / 1.0.2 的 `MPTCPKeychainBroker` v1 精确字节：

`sha256=5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`

Mac 构建入口和发布验证都会硬校验该值。重建、重签或替换 Broker v1 会直接使 release gate 失败。

## 发布验证

1.0.0 的正式 Stable 基线使用独立 `--stable-release` gate。1.0.3 使用 Stable patch gate：要求 frozen source、artifact hash、provenance 与当前源码 correctness evidence 自洽；本次记录 Go 全包测试、`go vet` 与完整 multipath race 通过，但 Scheduler 性能 promotion、capacity、180s runtime 与物理 WAN acceptance 不作为 1.0.3 的新发布结论。
