# MPTCP Userspace v1.1.1 架构

## 1. 系统边界

MPTCP Userspace 是应用层 multipath transport，不创建内核 MPTCP socket。

```text
Application
    |
    v
Client Engine
    |  MPX/4 Session / Streams
    |===============================|
    |                               |
TCP Carrier A                   TCP Carrier B ...
    |                               |
 Relay A                         Relay B
    |                               |
    +-------------+-----------------+
                  |
               Landing
                  |
             Backend TCP

Native UDP: Client <-> Relay/Landing MPU/1 path -> Backend UDP
UoT: UDP payload -> MPX product Stream -> TCP Carrier -> Landing -> Backend UDP
```

MPX/4 端点只有 Client 与 Landing。Relay 看不到 Stream 语义，也不持有 Transport Key。

## 2. Client

Client 有两个产品形态：

- macOS 的 **MPTCP Desk**；
- Linux headless Client。

二者共用同一套 Go MPX/4 engine。

一个 Profile 至少描述：

- 本地 `listen_port`；
- TCP/UDP 开关；
- Transport Key；
- Relay 列表；
- Scheduler；
- Weighted 模式下的方向容量先验。

Client 本地监听器把上层 TCP 连接映射为 MPX Stream。多个应用连接可以同时复用同一个 Session 和多条 Carrier。

## 3. Carrier

Carrier 是普通 TCP 连接。MPX/4 Stable 在 Carrier 字节流上完成认证握手、Secure Record、Frame 传输和 JOIN。

当前实现允许最多 8 条 active Carrier。协议 Carrier ID 使用 MPX VarInt 空间，因此 8 只是实现并发上限，不是协议编号上限。

Carrier 可以：

- 并行承载不同 Stream；
- 并行承载同一 Stream 的不同 DATA；
- 在路径断开后由其它 Carrier reinject 未完成的可靠数据；
- 乱序到达，再由 Stream/Transmission 语义恢复正确交付顺序。

## 4. Relay

Relay 是透明转发层，不参与 MPX/4 handshake 和 flow control。

它的职责是：

- 接收 Client 的普通 TCP；
- 把字节转发到 Landing listener；
- 提供独立网络路径、出口、运营商或地理位置。

### 推荐 TCP 设置

**Relay 推荐 BBR，并优先配合 `fq` qdisc。**

Relay 位于 Carrier 边缘，BBR pacing 有助于控制异构公网路径的持续排队。完整配置见 [NETWORK-TUNING.zh-CN.md](NETWORK-TUNING.zh-CN.md)。

Relay 不保存 Transport Key，也不应该解析 MPX Frame。

## 5. Landing

Landing 是 MPX/4 Server 端点。

它负责：

- 验证 Transport Key；
- 建立/加入 Session；
- 管理 Stream 和 Session WINDOW；
- 运行本地 Scheduler；
- 管理 retransmission/reinjection；
- 为应用 Stream 建立 backend TCP；
- 可选处理 Native UDP / UoT。

Landing 配置的 `max_sessions` 为 1–16，默认生成配置为 4。

### 推荐 TCP 设置

**Landing 推荐 CUBIC。**

Landing 是所有 Carrier 汇聚点。当前部署基线不建议默认把 Landing 也切成 BBR，而是让 Landing 使用 CUBIC，把多路径选择和 flight 控制交给 MPX/4 自己。

## 6. MPX/4 Stable

v1.1.1 固定使用：

- Wire Protocol Version：4；
- Capability Revision：8；
- Protocol Release：`protocol-v4.0.0`；
- Protocol source commit：`44f587fd279ed2238b070dd68114c76822353f4d`。

主要资源边界：

- `MaxPayload` = 32 KiB；
- `MaxRecordSize` = 64 KiB；
- `MaxStreams` = 2048；
- `MaxCarriers` = 8（本地 active 上限）；
- per-Stream receive window 最大 16 MiB；
- Session receive credit 128 MiB；
- receive physical accounting 128 MiB；
- DATA pending byte pool 1 GiB。

## 7. v1.1.1 Flow Control

发送端真正决定“现在能不能发 DATA”的协议级真值只有对端发布的：

- Stream WINDOW；
- Session WINDOW。

本地仍有资源保护，例如 pending frame/byte、Carrier flight/budget 和 receiver memory，但历史 `txUsed` / `txGrowth` 不再作为第二套发送 admission。

这样可以避免多 Stream 场景中 aggregate Session WINDOW 已经推进，却因为旧的本地 consumed/growth 账本滞后而产生额外 Session 级 HOL blocking。

## 8. v1.1.0+ 并发数据面

当前数据面不再把所有 Stream 工作都串在一个大 Session 临界区里。核心结构包括：

- per-Stream ready ring；
- dispatcher 有界批处理；
- Stream-local receive mutex；
- DATA payload store / overlap validation 与长 Session 临界区解耦；
- user Read copy 脱离 Session 全局锁；
- 同一 Stream 的跨 Carrier publication 有独立串行保护；
- targeted writer wake。

协议仍然需要共享 Session 状态，例如 Transmission ID、Session WINDOW、Carrier lifecycle；实现优化的目标是缩短这些共享状态的临界区，而不是把协议状态错误地拆成完全独立的线程局部状态。

## 9. Scheduler

Scheduler 是本地策略，不进入 Stable Core 的协商状态。

Client 侧策略决定 Client→Landing DATA 如何选择 Carrier；Landing 侧策略决定 Landing→Client DATA 如何选择 Carrier。因此两端可以不同，但通常推荐配置一致。

四种模式：Auto / Aggregate / Protect / Weighted。Weighted 把配置容量与实时 RTT、queue、delivery、penalty 结合，容量不是硬性比例。

## 10. Provisioning 与 Bundle

Provisioning 可以下发单 Profile 或 Bundle。

Parallel Bundle 中每个 Profile 有独立 runtime supervisor：

- 本地端口预检查是整组原子操作；
- 某一 Profile 远端连接/认证失败只影响该 Profile；
- 单 Profile 失败后按 1s → 2s → 5s → 10s → 30s → 每 30s 自动重试；
- 进入 listening 后清零退避；
- 全部 Profile 暂时断开时，Bundle supervisor 仍保持运行等待恢复。

## 11. macOS Broker 边界

MPTCP Desk 主 App 与 Keychain Broker 分离。Broker v1 字节保持冻结，用来维持 App 更新时的 Keychain 授权连续性。主 App 可以升级而不反复替换 Broker 身份。

## 12. 诊断思路

出现性能问题时按层看：

1. Client application / local listener；
2. MPX Stream/Session WINDOW；
3. Scheduler / Carrier queue / outstanding / reinjection；
4. Relay TCP 与 CPU；
5. **Relay 是否 BBR**；
6. Landing TCP 与 CPU；
7. **Landing 是否 CUBIC**；
8. backend。

不要把所有问题都归因于 MPX Scheduler。底层 TCP 拥塞控制、公网排队和 Relay/Landing CPU 都会改变 MPX 能看到的反馈。
