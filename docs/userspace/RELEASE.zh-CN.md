# MPTCP Userspace 1.0.1 / MPX/4 Protocol Version 4 Stable

1.0.1 是基于 1.0.0 Stable 的兼容补丁版本：MPTCP Desk、Linux Client、Landing 与 Provisioning 统一使用 **1.0.1**。Wire Protocol Version 仍为 4，协议源继续冻结到 MPX/4 `protocol-v4.0.0`，commit `44f587fd279ed2238b070dd68114c76822353f4d`。

## 1.0.1 变化

- 新 Stream 在 `STREAM_OPEN` 后立即发送的显式 WINDOW 从 32 KiB 提高到 192 KiB；Session 共享 credit 的 bootstrap accounting 基线仍为 32 KiB，超过部分继续计入共享 growth credit，Session 128 MiB hard limit 不变；
- Weighted 模式的 Carrier flight budget 改为仅使用 configured capacity 与 `minRTT`/base RTT 计算。排队抬高的 load RTT 不再扩大 flight budget，避免“排队 → RTT 上升 → budget 上升 → 继续加深排队”的正反馈；
- Keychain Broker 不升级，继续复用 0.10.12 / 1.0.0 的冻结 v1 二进制。

本版本按用户要求直接发布，不重跑性能/容量/WAN acceptance；发布包会明确标记为 `untested-by-request`，不继承新的验证结论。

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

1.0.1 **不升级 Broker**。继续复用 0.10.12 / 1.0.0 的 `MPTCPKeychainBroker` v1 精确字节：

`sha256=5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`

Mac 构建入口和发布验证都会硬校验该值。重建、重签或替换 Broker v1 会直接使 release gate 失败。

## 发布验证

1.0.0 的正式 Stable 基线使用独立 `--stable-release` gate。1.0.1 本次按用户要求走 `--untested-release`：仍要求 frozen source、artifact hash 与 provenance 自洽，但不声明 Go/vet/race、Scheduler、capacity、180s runtime 或物理 WAN acceptance 已为 1.0.1 重跑通过。
