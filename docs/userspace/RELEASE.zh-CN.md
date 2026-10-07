# MPTCP Userspace 1.0.5 / MPX/4 Protocol Version 4 Stable

1.0.5 是基于 1.0.4 的兼容性能回归版本：MPTCP Desk、Linux Client、Landing 与 Provisioning 统一使用 **1.0.5**。Wire Protocol Version 仍为 4，协议源继续冻结到 MPX/4 `protocol-v4.0.0`，commit `44f587fd279ed2238b070dd68114c76822353f4d`。

## 1.0.5 变化

- Weighted flight 直接恢复 0.10.7 / 0.10.9 / 0.10.12 已长期使用的有界 feedback RTT 公式：`feedback = min(4*baseRTT, max(baseRTT, currentFeedbackRTT))`；配置带宽仍决定目标 flight，但真实 MPX receipt RTT 最多只允许按 **4× base RTT** 参与预算；
- 完整移除 1.0.4 新增的 adaptive Weighted growth 状态与 receiver-clock epoch 扩窗逻辑，避免继续叠加额外控制环；load RTT 可以有限度补偿 delayed feedback，但不会无限扩大；
- `writer_turn` 恢复 0.10.x 的决策模型：当当前 shared credit / dynamic pending room 足够让所有活跃 writer 各拿一个完整 DATA turn 时不串行；真正资源不足时才按 FIFO 单 writer 传棒；
- writer-turn 的资源判断继续使用 1.0.x 的 **动态 shared growth room + 1 GiB DATA pending + dynamic bootstrap reserve**，不恢复 0.10.x 约 64 MiB 的静态 growth pending reserve；
- 继续保留 1.0.3 的 per-Stream targeted wake，不恢复旧版 wake-all；继续保留 1.0.2 的 1 GiB DATA pending、1.0.1 的 192 KiB OPEN bootstrap；
- `MaxPayload` 仍为 32 KiB，Session flow-control hard limit 仍为 128 MiB，Landing `MemoryMax` 仍为 2 GiB；
- Keychain Broker v1 继续冻结不变。

本版本通过当前源码 Go 全包测试、`go vet`、完整 multipath race，以及 source-matched Weighted high-BDP release gate。旧式有界 feedback 公式在 300 Mbps 高-BDP gate 中取得约 266 Mbps 中位吞吐；物理 WAN / App+Surge acceptance 不作为本版本的新发布结论。

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

1.0.5 **不升级 Broker**。继续复用 0.10.12 / 1.0.0 / 1.0.1 / 1.0.2 / 1.0.3 / 1.0.4 的 `MPTCPKeychainBroker` v1 精确字节：

`sha256=5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`

Mac 构建入口和发布验证都会硬校验该值。重建、重签或替换 Broker v1 会直接使 release gate 失败。

## 发布验证

1.0.0 的正式 Stable 基线使用独立 `--stable-release` gate。1.0.5 使用 Stable patch gate：要求 frozen source、artifact hash、provenance 与当前源码 correctness evidence 自洽；本次记录 Go 全包测试、`go vet`、完整 multipath race 与 source-matched Weighted high-BDP release gate 通过，但完整 Scheduler promotion、capacity、180s runtime 与物理 WAN acceptance 不作为 1.0.5 的新发布结论。
