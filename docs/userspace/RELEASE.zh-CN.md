# MPTCP Userspace 1.0.2 / MPX/4 Protocol Version 4 Stable

1.0.2 是基于 1.0.1 的兼容补丁版本：MPTCP Desk、Linux Client、Landing 与 Provisioning 统一使用 **1.0.2**。Wire Protocol Version 仍为 4，协议源继续冻结到 MPX/4 `protocol-v4.0.0`，commit `44f587fd279ed2238b070dd68114c76822353f4d`。

## 1.0.2 变化

- Landing/发送侧 DATA pending 总池从 128 MiB 提高到 **1024 MiB**，DATA pending frame 上限从 8192 提高到 **32768**；`MaxPayload` 仍保持 32 KiB，Session flow-control hard limit 仍为 128 MiB；
- 删除按 `MaxStreams=2048` 静态预留 pending 空间的旧逻辑，改为只给**当前真实活跃、且尚未完成首个 32 KiB bootstrap 的 writer**动态保留少量 frame/bytes；idle Stream 不再占 reserve；
- DATA enqueue 只 kick dispatcher，不再广播唤醒所有 writer；普通 DATA ACK 每释放一个 pending slot，只向 pending-blocked writer 发放对应 permit，减少高并发 Stream 下的 thundering herd 与 Session mutex 竞争；
- Landing systemd 安装模板的 `MemoryMax` 从 1G 提高到 **2G**，为 1 GiB pending pool 与 Go heap/元数据预留安全余量；
- 继承 1.0.1 的 192 KiB OPEN bootstrap 与 Weighted `minRTT` flight-budget 修复；Keychain Broker 继续冻结不变。

本版本通过当前源码的 Go 全包测试、`go vet` 与 pending/wakeup 相关 race 回归；未重新声明新的 WAN、capacity 或物理 App/Surge 性能 acceptance。

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

1.0.2 **不升级 Broker**。继续复用 0.10.12 / 1.0.0 / 1.0.1 的 `MPTCPKeychainBroker` v1 精确字节：

`sha256=5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`

Mac 构建入口和发布验证都会硬校验该值。重建、重签或替换 Broker v1 会直接使 release gate 失败。

## 发布验证

1.0.0 的正式 Stable 基线使用独立 `--stable-release` gate。1.0.2 使用 Stable patch gate：要求 frozen source、artifact hash、provenance 与当前源码 correctness evidence 自洽；本次记录 Go 全包测试、`go vet` 与 targeted race 通过，但 Scheduler 性能 promotion、capacity、180s runtime 与物理 WAN acceptance 不作为 1.0.2 的新发布结论。
