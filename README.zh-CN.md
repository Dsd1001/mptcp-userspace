# MPTCP Userspace

MPTCP Userspace 是一个运行在应用层的多路径传输系统，由 macOS 客户端 **MPTCP Desk**、Linux **Landing** 和可选的 **Provisioning 网页/API 平台**组成。它把多条普通 TCP Carrier 组合成一个经过认证的 MPX/4 Session，再把应用 TCP Stream 调度到这些 Carrier 上。

它**不是内核 MPTCP，也不是 QUIC**。macOS 与 Linux Userspace Client 都使用普通 TCP socket；Linux Landing 终止 MPX/4 并转发透明 backend TCP 字节。

当前正式版本：**v0.9.8 / MPX/4 Draft 04 + Provisioning**。

- Release：https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.9.8
- MPX/4 规范：https://github.com/Dsd1001/MPX-4
- English README：[README.md](README.md)

## 发布平台

| 组件 | 系统 / 架构 | Release 产物 |
|---|---|---|
| MPTCP Desk Client | macOS arm64 + x86_64 | `MPTCP-Desk-0.9.8-universal.dmg` |
| Headless Client | Linux amd64 | `mptcp-client-linux-amd64` |
| Headless Client | Linux arm64 | `mptcp-client-linux-arm64` |
| Landing | Linux amd64 | `mptcp-landing` |
| Landing | Linux arm64 | `mptcp-landing-linux-arm64` |
| Provisioning | Linux amd64 | `mpx-provision` |
| Provisioning | Linux arm64 | `mpx-provision-linux-arm64` |

Linux Client 只支持 `userspace_multipath`；Native MPTCP fallback 仍然只在 macOS 使用。

## 架构

```text
应用 / Surge
    |
    v
127.0.0.1:1081 本地透明 TCP 入口
    |
    v
MPTCP Desk / userspace engine
    |
    +-- 普通 TCP Carrier 1 --> Relay A --+
    +-- 普通 TCP Carrier 2 --> Relay B --+--> Linux Landing --> backend
    '-- 普通 TCP Carrier N --> Relay N --+
                 MPX/4 Session

Provisioning 管理网页
    |
    '-- HTTPS 私密配置 URL --> MPTCP Desk
```

Relay 只负责转发普通 TCP 字节，不需要理解 MPX/4。认证、Secure Record、Stream 复用、信用控制、调度、重传和跨 Carrier reinjection 都由 Mac 与 Landing 端到端完成。

## 0.9.8：MPX/4 Draft 04

0.9.8 对齐 MPX/4 仓库最新的 **Draft 04**，基准规范提交为 `5854899b63676eb8bb43048678ef99b4589170c3`。

Draft 04 刻意保持 Draft 03 的字节编码不变，主要把状态机语义正式收紧。0.9.8 实现了这些新增的规范要求：

- 每个已使用 Carrier ID 在整个 Session 生命周期内保留 Highest Accepted Generation；
- 一个未使用 Carrier ID 的第一条有效连接必须是 Generation 0；
- 更低 Generation 和相同 Generation 的复用都以 `CARRIER_CONFLICT` 拒绝，即使旧 TCP 已经断开也不能复用同一 incarnation；
- 更高 Generation 只有在候选 Carrier 完成认证建立后才提交；
- replacement 提交后，所有低 Generation incarnation 进入 SUPERSEDED，不能再产生新协议状态、路径样本或新的 Transmission Attempt；
- Generation 永不回绕；
- retransmission / reinjection / Carrier replacement 都保持原 Transmission ID；
- 从未分配过的 Transmission ACK 与已经 settled/压缩掉的旧 ACK 明确区分；
- `STREAM_OPEN_REJECT`、`CARRIER_CLOSE`、`SESSION_CLOSE` 使用 MPX/4 标准 Error Code 及 Draft 04 failure scope；
- FLOW_CONTROL_ERROR、FINAL_SIZE_ERROR、TRANSMISSION_ID_ERROR 和 established Stream-state error 按规范升级为 Session 级关闭；
- malformed authenticated Frame 属于 Carrier 级错误；认证/完整性失败只终止该 Carrier；
- Auto / Aggregate / Protect / Weighted 遵守 Draft 04 的 scheduler 语义边界，但具体算法仍由实现自行决定。

仓库除了既有 VarInt、Frame、key schedule、Secure Record 字节级测试外，还直接纳入最新规范仓库的 `carrier-generation.json` 与 `error-scope.json` 官方 Draft 04 语义向量。

## Provisioning 网页/API

0.9.8 正式包含完整的自托管 Provisioning 平台。Mac 可以只保存一条私密 HTTPS API URL，在每次启动前获取完整配置：

- Userspace MPX/4 / Native 模式；
- 本地监听端口；
- TCP / UDP 开关；
- Auto / Aggregate / Protect / Weighted；
- Relay IPv4 与端口；
- Weighted 上下行容量；
- 32 字节 MPX transport key；
- 后台常驻。

仓库提供 Linux `mpx-provision` 服务、内嵌管理网页、Docker 示例以及 Mac Managed Mode。API URL 与 transport key 均放在 macOS Keychain；托管模式获取权威 API 失败时不会偷偷用旧缓存启动。

## 调度策略

- **Auto**：从 Session / Carrier 实时状态选择本地运行策略；
- **Aggregate**：让多条 eligible Carrier 并发承担普通流量；
- **Protect**：可以偏好部分 Carrier，并保留其他 Carrier 做保护、重传和恢复；
- **Weighted**：把 `PATH_CAPACITY` 与实时 RTT、queue、penalty、delivery 等信号一起使用。

Weighted 中 `download_mbps` 必填，`upload_mbps` 可选；协议单位为 100,000 bit/s。配置容量只是 scheduler 输入，不是 flow-control credit，也不是保证速率。

## 兼容性

Draft 04 保持 Draft 03 的 wire encoding，但 0.9.8 同时修正并强制执行新的 Generation / Error Code 语义，因此正式支持的组合是 **Mac 0.9.8 + Landing 0.9.8**。

| Mac | Landing | 状态 |
|---|---|---|
| 0.9.8 | 0.9.8 | 正式支持，MPX/4 Draft 04 |
| 0.9.8 | 0.9.6 / 0.9.7-derived build | Version 4 字节格式相同，但混合语义不作为正式支持组合 |
| 0.9.8 | 0.9.5 或更早 | 不兼容，MPX/3 |

升级两端后，原有 Relay 地址、端口、scheduler 和 64 位十六进制 transport key 可以继续使用。

UDP 继续使用**独立 MPU/1 数据报平面**。Draft 04 没有把本项目的 UDP 模式定义成 MPX/4 Core Datagram 扩展，MPX/4 Weighted 容量也不作用于 MPU/1。

## 资源边界

- 一个 Session 最多 8 条 Carrier；
- 最多 2048 条活跃 peer-initiated Stream；
- STREAM_DATA 最大 32 KiB；
- 单 Stream 最大信用窗口 16 MiB；
- Session 信用窗口 128 MiB；
- sender DATA/control queue 有硬上限；
- physical receive-page accounting 上限 128 MiB。

Carrier 丢失本身不会终止 Stream。未确认的 reliable Transmission 可以重新调度、重传或跨 Carrier reinjection，并保持原 Transmission ID。

## 安全模型

MPX/4 Draft 04 使用 32 字节预共享 transport key 作为认证根，使用 HKDF-SHA256/HMAC-SHA256 完成 key derivation 与 Finished，Secure Record 使用 AES-256-GCM。每条认证 Carrier 都派生 fresh 双向 traffic key 和 IV。

这不是 TLS PKI，Draft 04 不提供 forward secrecy。transport key 与 Provisioning API URL 都属于凭据，不应进入仓库、Issue 或普通日志。发布的 macOS 应用使用 ad-hoc 签名，未做 Developer ID notarization。

## 构建

```sh
cd macos/engine
go test ./...

MPTCP_GO=/path/to/go ./macos/build.sh
MPTCP_GO=/path/to/go ./scripts/build-linux-client.sh
MPTCP_GO=/path/to/go ./scripts/build-userspace-landing.sh
MPTCP_GO=/path/to/go ./scripts/build-provisioning.sh
```

## 文档

- [MPX/4 Draft 04 实现说明](docs/userspace/PROTOCOL.md)
- [0.9.8 Release Notes](docs/userspace/RELEASE.zh-CN.md)
- [Linux Headless Client](docs/userspace/LINUX-CLIENT.md)
- [完整 Provisioning API](docs/userspace/PROVISIONING.md)
- [调度模式](docs/userspace/SCHEDULER-MODES.md)
- [部署与回滚](docs/userspace/DEPLOYMENT.zh-CN.md)
- [快速开始](docs/guides/QUICKSTART.zh-CN.md)
- [故障排查](docs/guides/TROUBLESHOOTING.zh-CN.md)
