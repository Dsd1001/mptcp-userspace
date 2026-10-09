# MPTCP Userspace

MPTCP Userspace 是一套运行在 macOS / Linux 上的**应用层多路径传输系统**。它把多条普通 TCP 连接组织成一个经过认证的 **MPX/4 Protocol Version 4 Stable** Session，在 Session 内承载多个应用 Stream，并在路径变慢、丢包或断开时支持跨 Carrier 重传与 reinjection。

当前正式版：**v1.1.4**
协议源：**MPX/4 `protocol-v4.0.0`**（`44f587fd279ed2238b070dd68114c76822353f4d`）

它**不是内核 MPTCP**，也**不是 QUIC**。Relay 只负责透明转发 Carrier 字节，不需要 MPX Transport Key；认证、加密、Stream 状态、Flow Control、Scheduler 与可靠数据重注入都发生在 Client 与 Landing 两个 MPX 端点之间。

[English](README.md) · [文档索引](docs/README.zh-CN.md) · [v1.1.4 Release](https://github.com/Dsd1001/mptcp-userspace/releases/tag/v1.1.4) · [MPX/4 协议仓库](https://github.com/Dsd1001/MPX-4)

## 推荐生产拓扑

```text
应用程序
   |
   v
MPTCP Desk / Linux Client
   |
   | 多条普通 TCP Carrier
   +--------> Relay A --+
   +--------> Relay B --+--> Landing --> Backend TCP/UDP
   +--------> Relay C --+
```

### 强烈推荐的拥塞控制基线

当前实现推荐：

- **Landing：CUBIC**
- **Relay：BBR**，Relay 主机建议配合 `fq` qdisc

这不是 MPX/4 协议强制要求，而是本项目针对当前架构给出的部署基线。Relay 位于每条 Carrier 的转发边缘，BBR 的 pacing 与带宽/RTT 模型更适合控制异构链路上的排队；Landing 是多条 Carrier 的汇聚端，默认使用 CUBIC 可以避免在汇聚点再叠加一层模型型拥塞控制，让 MPX/4 自己的多路径调度、RTT/queue 反馈与 flight budget 更容易保持稳定和可解释。

**不要把所有机器统一改成 BBR。默认建议就是 Landing=CUBIC、Relay=BBR。** 完整 sysctl、持久化配置、验证和回滚方法见 [网络与拥塞控制调优](docs/guides/NETWORK-TUNING.zh-CN.md)。

## 组件

| 组件 | 作用 |
| --- | --- |
| **MPTCP Desk** | macOS / Windows Userspace GUI Client，支持本地/远端配置、诊断、后台恢复、Sparkle 更新和可选远程设备管理。 |
| **Linux Client** | 无 GUI Client，与 MPTCP Desk 共用同一套 MPX/4 Go Engine。 |
| **Relay** | Client 与 Landing 之间的普通 TCP 转发节点，不是 MPX 端点，不持有 Transport Key。 |
| **Landing** | MPX/4 Server 端点，终止 Carrier Session，并把 Stream 转发给 backend。 |
| **Provisioning** | 可选的 HTTPS 配置和远程管理服务，管理 Profile、Bundle 与设备。 |

## v1.1.2 的核心变化

- **Queue-aware Admission**：发送端默认限制每个 Session 的未调度 DATA，32 MiB 软目标，大流约 28 MiB 后等待，给新 Stream 留出首包容量。
- **可回退**：环境变量 MPX_QUEUE_ADMISSION_MIB=0 关闭新版队列控制；16/32/64 可用于对照测试。保留原来的 Peer WINDOW、128 MiB Session Credit、重传账本。
- **macOS 只有 Userspace**：删除内核 MPTCP 切换、系统聚合开关及系统授权操作；旧 Native 配置不会被悄悄转换，必须手动配置 MPX/4 Transport Key。
- **版本统一**：macOS、Windows、Linux Client、Landing、Provisioning 均为 v1.1.2；Keychain Broker v1 仍冻结。
- 六条 92 Mbps 模拟 Relay、150 Stream 的对照测试通过，但暂未完成生产 WAN A/B，不承诺提升带宽。

## v1.1.1 的流控变化

v1.1.1 保留 v1.1.0 的多 Stream 并发数据面重构，同时简化发送侧 Flow Control：

- 对端发布的 `STREAM_WINDOW` 和 `SESSION_WINDOW` 成为发送许可的权威来源；
- 历史 `txUsed` / `txGrowth` 继续保留为诊断账本，但不再形成第二套 128 MiB 发送 admission；
- 继续保留本地 pending frame/byte、receiver memory、Carrier flight/budget 等资源硬保护。

MPX/4 Stable wire 语义没有变化。当前实现主要边界为：

- 最多 **8 条 active Carrier**；
- 最多 **2048 个 Stream**；
- 单个 STREAM_DATA 最大 **32 KiB**；
- 单 Stream 最大 receive window **16 MiB**；
- Session receive-credit **128 MiB**；
- physical receive-buffer accounting **128 MiB**；
- DATA pending-byte pool **1 GiB**；
- Session-wide Transmission ID 与跨 Carrier retransmission/reinjection。

Carrier ID 的协议编号空间并不是只有 8 个；8 是当前实现的本地同时 active Carrier 上限。

## Scheduler

当前有四种本地调度策略：

- **Auto**：通用模式，根据路径健康、负载和反馈决定如何使用 Carrier；
- **Aggregate**：更积极地让多条 eligible Carrier 并发承担 DATA；
- **Protect**：限制异常路径，只保留有界探测与恢复机会；
- **Weighted**：把用户填写的上下行容量先验与实时 RTT、queue、delivery、penalty 等一起参与选择。

Scheduler 是端点本地策略，不是 MPX/4 Stable Core 的 wire 协商 ID。Client 和 Landing 可以不同，但生产环境通常建议两端使用同一模式，便于理解上下行行为。Weighted 的容量不是固定百分比分流，也不是带宽保证。

## TCP、UDP 与 UoT

应用 TCP 通过 MPX/4 Stream 运行在认证后的 TCP Carrier 上。

UDP 有两种产品路径：

- **Native UDP**：独立的认证 MPU/1 数据面；
- **UoT**：把 UDP payload 放入已有的认证 TCP Carrier Session。

因此 Landing=CUBIC / Relay=BBR 主要影响 TCP Carrier、UoT 和普通 backend TCP socket；Native UDP 不受 Linux TCP congestion control 控制。

## 快速开始

1. 下载 v1.1.1 Release，并校验 `MPTCP-Userspace-1.1.1-SHA256SUMS`。
2. 安装 Landing，配置 backend、Transport Key、Scheduler 和 `max_sessions`。
3. **把 Landing 设置为 CUBIC。**
4. 配置各 Relay 转发到 Landing，并**把 Relay 设置为 BBR**。
5. 在 MPTCP Desk 或 Linux Client 创建本地 Profile，或者使用 Provisioning URL。
6. 启动后确认多条 Carrier 已连接，再做真实业务测试。

详细步骤见 [快速开始](docs/guides/QUICKSTART.zh-CN.md)。

## v1.1.1 发布文件

```text
MPTCP-Desk-1.1.1-universal.dmg
mptcp-client-linux-amd64
mptcp-client-linux-arm64
mptcp-landing
mptcp-landing-linux-arm64
mpx-provision
mpx-provision-linux-arm64
MPTCP-Userspace-1.1.1-SHA256SUMS
MPTCP-Userspace-1.1.1-source.tar.gz
MPTCP-Userspace-1.1.1-release.tar.gz
PROVENANCE.json
TESTS.json
RUNTIME.json
CAPACITY.json
SCHEDULER-MODES.json
ACCEPTANCE.md
```

v1.1.1 完成了 correctness / build / concurrency 回归，但发布时没有重新宣称新的物理 WAN 吞吐或 capacity 基准。因此生产环境仍需要在真实 Relay/Landing 链路上验证性能。

## 文档入口

建议按这个顺序阅读：

- [快速开始](docs/guides/QUICKSTART.zh-CN.md)
- [网络与拥塞控制调优：Landing CUBIC / Relay BBR](docs/guides/NETWORK-TUNING.zh-CN.md)
- [系统架构](docs/guides/ARCHITECTURE.zh-CN.md)
- [部署、升级与回滚](docs/userspace/DEPLOYMENT.zh-CN.md)
- [故障排查](docs/guides/TROUBLESHOOTING.zh-CN.md)
- [MPX/4 实现说明](docs/userspace/PROTOCOL.md)
- [Scheduler 模式](docs/userspace/SCHEDULER-MODES.md)
- [Linux Client](docs/userspace/LINUX-CLIENT.md)
- [Provisioning](docs/userspace/PROVISIONING.md)
- [v1.1.1 Release Notes](docs/userspace/RELEASE.zh-CN.md)

旧的 MPX/3、0.7.x 和 pre-Stable 文档只保留用于实现考古，不再作为当前部署指南。

## 安全边界

- 64 位十六进制 Transport Key 是敏感凭据；
- 完整 Provisioning URL 是 bearer credential；
- 非 loopback Provisioning 必须使用 HTTPS；
- 不要把 Transport Key、Authorization header 或完整 secret URL 写进公开日志；
- Landing 私有配置文件必须为 `0600`/`0400`，或者通过受管理的 systemd credential 路径提供。

## 性能边界

MPTCP Userspace 解决的是应用层多路径传输与可靠性，不会自动消除运营商拥塞、bufferbloat、Relay CPU 瓶颈、backend 瓶颈或错误的 Linux TCP 参数。多路径吞吐也不保证等于各线路标称带宽的简单相加。

生产调优时，先固定推荐基线 **Landing=CUBIC / Relay=BBR**，再观察 RTT、goodput、queue、outstanding、retransmit、Stream/Session WINDOW 和系统 CPU，而不是一开始就同时修改多组内核参数。
