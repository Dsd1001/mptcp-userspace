# MPTCP Userspace

MPTCP Userspace 是一个面向 macOS 与 Linux 的**应用层多路径传输系统**。它把多条普通 TCP Carrier 组成一个经过认证的 **MPX/4** Session，把应用 TCP Stream 多路复用到这些 Carrier 上，并在路径退化或断开时对可靠数据进行重传与跨路径 reinjection。

它**不是内核 MPTCP，也不是 QUIC**。Relay 只需要转发普通 TCP 字节；MPX/4 的认证、加密、Stream 状态、流控和调度都由 Client 与 Landing 端到端完成。

**当前正式版本：v0.10.7 · MPX/4 Draft 04**

- [最新 Release](https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.10.7)
- [MPX/4 规范仓库](https://github.com/Dsd1001/MPX-4)
- [English README](README.md)

## 0.10.7：Mac 与 Provisioning 前端界面更新

0.10.7 正式带入新版 **MPTCP Desk** 界面与重新设计的 **Provisioning** 管理后台。导航、状态展示、配置编辑和设备管理视图都重新整理，但底层 Profile/Bundle 与 Device Control API 语义不变。

0.10.6 的 Sparkle 2 签名更新与可选远程设备管理、0.10.5 的 Parallel Profile 自动重连、0.10.4 的 LKG 本地缓存机制全部保留。**MPX/4 继续使用 Draft 04 / WireProtocol 4 / CapabilityRevision 4**，本次 patch release 不修改 Scheduler、flow-control 或 key schedule。

## 项目包含什么

| 组件 | 作用 | 发布平台 |
|---|---|---|
| **MPTCP Desk** | macOS GUI Client 与本地透明 TCP 入口 | macOS arm64 + x86_64 Universal |
| **Headless Client** | 无 GUI 的 Client/runtime | Linux amd64 + arm64 |
| **Landing** | 终止 MPX/4，并为逻辑 Stream 打开 backend TCP | Linux amd64 + arm64 |
| **Provisioning** | 可选的网页/API 配置控制面，管理 Profile 与 Bundle | Linux amd64 + arm64 |
| **Relay** | 普通 TCP 转发节点，不需要理解 MPX/4 | 任意兼容 TCP 转发器 |

Release 同时提供 Universal DMG、Linux Client、Landing、Provisioning、冻结源码、SHA256、构建信息、provenance 与验证记录。

## 架构

~~~text
应用 / Surge
      |
      v
127.0.0.1:<listen_port>
      |
      v
MPTCP Desk / Headless Client
      |
      |  已认证 MPX/4 Session
      |
      +-- TCP Carrier 1 --> Relay A --+
      +-- TCP Carrier 2 --> Relay B --+--> Landing --> backend
      '-- TCP Carrier N --> Relay N --+

可选 Provisioning
      |
      +-- Profile URL --> 一份完整运行配置
      '-- Bundle URL  --> 多份 Profile
                         |-- single_select
                         '-- parallel
~~~

一份 **Profile** 拥有自己的本地监听端口、MPX Session、Relay/Carrier 集合、Scheduler、Transport Key 与 TCP/UDP 设置。一份 **Bundle** 可以包含 1–32 个 Profile，用于本机选择或并行运行。并行 Profile 始终是独立 Session，不会把不同 Profile 的 Relay 合并成一套路径池。

## 主要能力

- 基于普通 TCP Carrier 的 **MPX/4 Draft 04**，支持认证 CREATE/JOIN 与 Carrier Generation replacement。
- **Stream 多路复用**、Session/Stream Credit、受限内存资源模型与可靠 Transmission ID。
- **跨 Carrier 重传与 reinjection**，逻辑 Stream 字节身份不随路径改变。
- 四种调度策略：**Auto / Aggregate / Protect / Weighted**。
- Weighted 容量先验与实时 RTT、queue、delivery、penalty、路径可用性共同参与调度。
- **Parallel Bundle 故障隔离**：本地端口原子预检查通过后，某个 Profile 不通不会把其他健康 Profile 一起停掉。
- **Provisioning 不透明响应**：公网 Profile/Bundle URL 返回 AES-256-GCM 封装，不再直接展示 Relay IP/端口/Transport Key JSON。
- **远端 Bundle 逐 Profile 完整诊断**：Scheduler、重排/等待确认/重传、资源窗口、RTT、Goodput、queue、outstanding、error 等各自独立。
- 客户可见的远端路径诊断隐藏 Relay IP/端口和原始 endpoint 错误。
- macOS 可选**后台常驻**，支持登录、睡眠唤醒、网络恢复与 engine 异常后的重建。
- UDP 启用时使用独立的认证 **MPU/1** 数据面；它不是 MPX/4 Core Datagram 扩展。

## Provisioning 模型

Provisioning 是可选控制面，不经过业务数据路径。

一份 Profile 是完整、权威的运行配置，包括：

- 传输模式；
- 本地 listen_port；
- TCP/UDP 开关；
- Scheduler；
- 当前 **2–8 条 Relay**；
- Weighted 路径容量；
- MPX Transport Key；
- 后台常驻设置。

一份 Bundle 可以包含 1–32 个 Profile：

- single_select：一次只运行一个 Profile；
- parallel：可以同时运行一个或多个 Profile。

Parallel 启动前先对本地监听端口做**原子预检查**。重复端口、被占用端口等属于配置错误，会阻止整组启动；预检查通过后，各 Profile 的远端连接/认证故障独立处理，一个失败不会停止其他健康 Profile。

macOS 把 secret Provisioning URL 保存在 Keychain。Linux 可以通过 stdin 使用 validate-managed / run-managed，避免 bearer URL 出现在进程参数中。

## 路径诊断

MPTCP Desk 展示的是 Session 与路径运行状态，而不是单一“测速数字”。

默认直接显示：

- 配置/当前 Scheduler 与自动切换次数；
- TCP 载路、逻辑连接、上传/下载；
- 当前重排、重排峰值、等待确认；
- TCP 重传、UDP 丢弃/超时；
- 每条路径 RTT、Goodput、queue、outstanding 与错误计数。

深度资源信息分为两个默认收起的面板：

- **Stream / 生命周期资源**
- **Window / Credit 资源**

远端 Bundle 中每个 Profile 都有自己独立的一套诊断与展开状态。

## 当前实现边界

v0.10.7 当前实现限制为：

- 每个 Profile **2–8 条 Relay**；
- 每个 MPX/4 Session **最多 8 条 Carrier**；
- 最多 **2048 条 active peer-initiated Stream**；
- STREAM_DATA 最大 **32 KiB**；
- 单 Stream 最大 receive-credit window **16 MiB**；
- Session receive-credit window **128 MiB**；
- physical receive-page accounting 上限 **128 MiB**；
- sender DATA/control queue 有明确硬上限。

这些是当前实现上限，不代表 MPX/4 每个字段理论编码范围的上限。

## Scheduler

- **Auto**：默认保持聚合行为，在稳定证据表明路径退化时可进入保护行为。
- **Aggregate**：让多条 eligible Carrier 根据实时路径成本共同承担普通 DATA。
- **Protect**：健康路径继续承担业务，退化路径只保留受限探测/恢复。
- **Weighted**：在同样的实时安全信号上加入用户配置的方向容量。

Weighted 的 download_mbps 必填，upload_mbps 可选；未填上行容量时可以由实现自行估计。配置容量只是 Scheduler 输入，不是流控信用，也不是保证速率。

## 兼容性

当前正式支持组合是 **0.10.7 Client + 0.10.7 Landing + 0.10.7 Provisioning**。

0.10.7 完整沿用 0.10.5 的 MPX/4 Draft 04 数据面、Carrier Generation/Error Scope、Scheduler、flow-control 与 key schedule 语义；本次 patch release 只更新 macOS 与 Provisioning 前端界面，0.10.6 引入的签名更新和远程设备控制语义保持不变。0.10.7 Client 继续接受旧的明文 schema-1/schema-2 Provisioning 响应用于迁移；0.10.2 及之后的 Provisioning 默认返回不透明加密封装。

MPX/4 之前的版本和相关设计文档仍保留在仓库历史中，但不应再作为当前部署说明。

## 安全模型

MPX/4 Draft 04 使用 32 字节预共享 Transport Key 作为认证根，使用 HKDF-SHA256/HMAC-SHA256 完成派生与 Finished，Secure Record 使用 AES-256-GCM。每条已认证 Carrier 都会派生新的双向 traffic key 与 IV。

Provisioning 的公网响应封装使用现有高熵 URL Secret 作为密钥材料，不增加设备注册或第二套密码。因此**拿到完整 Provisioning URL 的人仍然拥有配置访问能力**。远程 Provisioning 必须使用 HTTPS，并把 URL 与 Transport Key 都视为凭据。

它不是 TLS PKI；Draft 04 也不提供 forward secrecy。当前发布的 macOS App 使用 ad-hoc 签名，未做 Developer ID notarization。

## 快速开始

- [快速开始](docs/guides/QUICKSTART.zh-CN.md)
- [Quick Start](docs/guides/QUICKSTART.md)
- [架构说明](docs/guides/ARCHITECTURE.zh-CN.md)
- [故障排查](docs/guides/TROUBLESHOOTING.zh-CN.md)

## 从源码构建

~~~sh
cd macos/engine
go test ./...

MPTCP_GO=/path/to/go ./macos/build.sh
MPTCP_GO=/path/to/go ./scripts/build-linux-client.sh
MPTCP_GO=/path/to/go ./scripts/build-userspace-landing.sh
MPTCP_GO=/path/to/go ./scripts/build-provisioning.sh
~~~

完整流程见 [从源码构建](docs/guides/BUILDING.zh-CN.md)。

## 文档

- [MPX/4 Draft 04 实现说明](docs/userspace/PROTOCOL.md)
- [Provisioning Profile / Bundle / 加密响应](docs/userspace/PROVISIONING.md)
- [Linux Headless Client](docs/userspace/LINUX-CLIENT.md)
- [调度模式](docs/userspace/SCHEDULER-MODES.md)
- [部署与回滚](docs/userspace/DEPLOYMENT.zh-CN.md)
- [验证与发布边界](docs/userspace/VALIDATION.md)
- [v0.10.7 Release Notes](docs/userspace/RELEASE.zh-CN.md)

仓库中带有 MPX/2、MPX/3 或旧版本号的文档保留用于历史与实现考古，不是当前协议/部署指南。
