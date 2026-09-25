# MPTCP Userspace 架构说明

## 1. 组件

MPTCP Userspace 由四类角色组成：

1. **应用 / Surge**：产生实际 TCP 业务。
2. **MPTCP Desk（Mac）**：接收本地透明 TCP 连接，把逻辑流封装进 MPX/3。
3. **Relay**：只做普通 TCP 字节转发，不理解 MPX/3。
4. **Landing（Linux）**：认证并终止 MPX/3，将解复用后的业务 TCP 连接到 backend。

示意：

~~~text
            +------------------- macOS -------------------+
            |                                             |
App/Surge ->| 127.0.0.1:1081 -> MPTCP Desk Userspace     |
            |                         |                    |
            +-------------------------|--------------------+
                                      |
                     +----------------+----------------+
                     |                |                |
                  Relay A          Relay B          Relay C
                     |                |                |
                     +----------------+----------------+
                                      |
                                Linux Landing
                                      |
                                   Backend
~~~

## 2. 为什么不是内核 MPTCP

Userspace 模式不会依赖内核 MPTCP subflow。

每条 carrier 是普通 TCP 连接，MPX/3 在应用层完成：

- session 认证；
- 多 carrier 归并；
- 逻辑 stream 标识；
- DATA / ACK / WINDOW 等记录；
- scheduler；
- flow control；
- timeout / retransmission / reinjection。

因此 Relay 只需要能够稳定转发 TCP，不需要支持 MPTCP 协议扩展。

Native 模式与 Userspace 模式是两条独立路径，不应混为一谈。

## 3. 一条业务流如何通过系统

当上层应用打开一个 TCP 连接时：

1. 本地入口接收业务连接；
2. Mac 为它分配 MPX/3 logical stream identity；
3. OPEN 在会话中建立逻辑流；
4. DATA 按 scheduler 分配到一条或多条 carrier 的发送机会；
5. Landing 解密并按 stream/offset 重组；
6. Landing 与 backend 建立/维护对应 TCP；
7. 反方向按同样的 MPX/3 机制返回。

逻辑流不是“一条业务 TCP 永远绑定一条 Relay”。调度器可以根据当前路径状态选择 DATA 所走的 carrier，必要时执行跨路 reinjection。

## 4. 会话与 carrier

同一个 MPX/3 session 可以拥有多条 carrier。

每条 carrier 在加入 session 时通过认证 hello 绑定：

- session / carrier 身份；
- challenge；
- configured scheduler；
- Rev5 Weighted 方向容量字段。

Weighted 的方向容量属于 HMAC transcript，因此中间 Relay 不能静默篡改。

0.9.4 使用 capability revision 5：

- 0x41 Auto
- 0x42 Aggregate
- 0x43 Protect
- 0x44 Weighted

其中前三个值与 0.9.3 保持兼容。

## 5. 调度器看到什么

调度器不是只看单一 RTT 或单一带宽值。

典型输入包括：

- carrier 是否连接；
- base RTT / 当前 RTT；
- writer queue；
- 已发送未确认 DATA 债务；
- delivery 速率；
- path role；
- penalty；
- stale / delivery timeout；
- configured capacity（Weighted）。

### Weighted

Weighted 把用户提供的容量作为“正常 DATA 调度的容量先验和 flight budget 基础”。

它不取消动态保护。因此：

- 路径断开时不会继续调度；
- penalty 期间有健康路径时会避开；
- queue 太深会增加预计到达成本；
- RTT 上升会影响 ETA；
- delivery timeout 仍会触发保护和 reinjection。

所以 Weighted 的目标是“让已知容量参与调度”，而不是“强制每条路径永远按固定百分比分流”。

## 6. Protect / Auto 的路径角色

Protect 以及 Auto 进入保护行为后，会使用路径角色：

- LEARNING：样本不足；
- ACTIVE：健康业务路径；
- PROBE：受限探测；
- BACKUP：默认不承担普通业务 DATA，仅保留有界恢复探测。

这套机制用于避免一条明显异常的路径继续积累大量未确认 DATA。

详细阈值和滞回见 [SCHEDULER-MODES.md](../userspace/SCHEDULER-MODES.md)。

## 7. Flow control

MPX/3 同时存在：

- per-stream WINDOW；
- session-level SESSION_WINDOW / MAX_DATA；
- sender DATA pending 限制；
- physical receive page accounting。

v0.9.4 的主要上限：

- 2048 occupied stream identities；
- 128 MiB session credit；
- 128 MiB sender DATA pending；
- 128 MiB physical receive accounting；
- 16 MiB per-stream receive window；
- 32 KiB DATA payload。

这些限制彼此不是同一个计数器。比如 session credit 仍有空间，不代表物理接收页一定还有空间。

详细说明见 [MPX3-CREDIT.md](../userspace/MPX3-CREDIT.md)。

## 8. 可靠性与跨路恢复

普通 DATA ACK 表示“数据已由对端协议层收到”，不等于 backend 应用已经消费。

当 carrier 交付停滞、断开或超时时，已有机制负责：

- 识别 stale / timeout；
- 降低或移除路径资格；
- 对必要 DATA 进行 retransmission；
- 在其他健康路径上 reinject。

因此某条 Relay “短时间摸鱼”不一定立即导致整个 session 中断，但可能表现为：

- 该路径业务量迅速下降；
- role 降级；
- penalty；
- retransmission / reinjection 上升。

## 9. RTT 与满载排队

空载 RTT 低、满载 RTT 明显升高通常说明链路中存在排队。

调度器会看到路径 RTT / queue 等实时信息，并把它们纳入路径成本；Weighted 也不会绕过这些安全信号。

但调度器不能从根本上消除运营商、Relay 或出口设备中的 bufferbloat。若所有路径都在满载下产生严重排队，仍需要从链路容量配置、发送负载或队列管理层面处理。

## 10. 安全边界

MPX/3 使用 PSK 认证与 AES-GCM 保护记录，但：

- 不是 TLS PKI；
- 0.9.4 不提供 forward secrecy；
- 不应把 transport key 放进仓库或公开日志；
- Relay 看见的是普通 TCP carrier，不需要持有协议密钥。

更精确的 wire format 见 [PROTOCOL.md](../userspace/PROTOCOL.md)。
