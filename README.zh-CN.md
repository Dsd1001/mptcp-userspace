# MPTCP Userspace

MPTCP Userspace 是一个运行在应用层的多路径传输系统，由 macOS 客户端 **MPTCP Desk** 和 Linux **Landing** 组成。它把多条普通 TCP Carrier 组合成一个经过认证的 MPX Session，再将应用 TCP Stream 调度到这些 Carrier 上。

它**不是内核 MPTCP，也不是 QUIC**。macOS Userspace 模式使用普通 TCP socket；Linux Landing 终止 MPX/4 并转发透明 backend TCP 字节。

当前正式版本：**v0.9.6 / MPX/4 Draft 03**。

下一候选版本：**v0.9.7**，新增完整 Provisioning：Mac 只需保存一个私密 API 链接，即可获取全部运行配置；仓库同时提供自托管 Provisioning Server 与管理网页。

- Release：https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.9.6
- MPX/4 规范：https://github.com/Dsd1001/MPX-4
- English README：[README.md](README.md)

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
```

Relay 只负责转发普通 TCP 字节，不需要理解 MPX/4。认证、Secure Record、Stream 复用、信用控制、调度、重传和跨 Carrier reinjection 都由 Mac 与 Landing 端到端完成。

## 0.9.6：升级到 MPX/4 Draft 03

0.9.6 的 TCP Userspace 线协议已从 MPX/3 切换到公开的 **MPX/4 Draft 03**，包括：

- canonical MPX VarInt；
- `CLIENT_INIT / SERVER_INIT / CLIENT_FINISHED / SERVER_FINISHED` 握手；
- 32 字节 transport key 作为 PSK 认证根；
- HKDF-SHA256 key schedule 与 HMAC-SHA256 Finished；
- AES-256-GCM Secure Record，双向独立 sequence space；
- typed Frame 与一个 Secure Record 内多 Frame batching；
- Session CREATE 与 Carrier JOIN；
- Carrier ID + 单调递增 Carrier Generation；
- 可靠有序 Stream、Stream/Session 双层显式 credit；
- 保持 Transmission ID 的重传与跨 Carrier reinjection；
- Auto / Aggregate / Protect / Weighted scheduler 协商；
- 微秒级 Receiver Timestamp 的路径 delivery feedback。

实现已对照 MPX/4 仓库 Draft 03 的 VarInt、Frame encoding、key schedule 和连续 Secure Record 官方 test vectors 做逐字节验证。

## 调度策略

原有成熟调度内核继续保留在新的 MPX/4 wire layer 之上：

- **Auto**：根据实际路径表现自动学习和保护；
- **Aggregate**：并发使用满足条件的 Carrier；
- **Protect**：优先使用主路径集合，同时保留备用保护路径；
- **Weighted**：将配置带宽与实时 RTT、queue、penalty、delivery timeout 等信号结合。

Weighted 中每条 Relay 的 `download_mbps` 必填，`upload_mbps` 选填；容量通过 MPX/4 `PATH_CAPACITY` 参数传输，单位为 100,000 bit/s。

## 兼容性

0.9.6 更换了 TCP wire protocol，因此 **MPTCP Desk 与 Landing 必须同时升级到 0.9.6**。

| Mac | Landing | TCP Userspace |
|---|---|---|
| 0.9.6 | 0.9.6 | 支持，MPX/4 Draft 03 |
| 0.9.6 | 0.9.5 或更早 | 不兼容 |
| 0.9.5 或更早 | 0.9.6 | 不兼容 |

原 profile 中 Relay 地址、端口、scheduler 以及 64 位十六进制 transport key 可以继续使用，但协议两端必须一起升级。

UDP 在 0.9.6 中继续使用**独立 MPU/1 数据报平面**，目前没有作为 MPX/4 Core Datagram 扩展合并进去，也不使用 Weighted 容量值。

## 资源边界

0.9.6 不通过扩大原有资源上限获得性能：

- 一个 Session 最多 8 条 Carrier；
- 最多 2048 条活跃 peer-initiated Stream；
- STREAM_DATA 最大 32 KiB；
- 单 Stream 最大信用窗口 16 MiB；
- Session 信用窗口 128 MiB；
- sender DATA/control pending 均有硬上限；
- physical receive-page accounting 上限 128 MiB。

单条 Carrier 失效时，只要还有其他 Carrier 可用，逻辑 Session 与 Stream 继续存活。未确认 Transmission 会重新进入 scheduler 进行 retransmission/reinjection。重建同一逻辑 Carrier 时使用更高 Generation，并重新完成 MPX/4 JOIN 和密钥派生。

## macOS 客户端

MPTCP Desk 支持 macOS 13+ arm64 / x86_64。0.9.5 引入的后台常驻能力全部保留，包括登录项、sleep/wake 恢复、网络恢复监听以及有界重启退避。

默认 Userspace TCP 本地入口为 `127.0.0.1:1081`。**它不是 SOCKS5 服务。**

## 安全模型

MPX/4 Draft 03 使用 32 字节预共享 transport key 作为认证根。每条 Carrier 都独立执行完整握手，并为 Client→Server / Server→Client 派生独立 application traffic key 与 IV。

这不是 TLS PKI，0.9.6 也不提供 forward secrecy。transport key 不应提交到仓库、Issue 或日志。发布的 macOS 应用使用 ad-hoc 签名，未做 Developer ID notarization。

## 构建

```sh
cd macos/engine
go test ./...

MPTCP_GO=/path/to/go ./macos/build.sh
MPTCP_GO=/path/to/go ./scripts/build-userspace-landing.sh
```

## 文档

- [MPX/4 实现说明](docs/userspace/PROTOCOL.md)
- [0.9.6 Release Notes](docs/userspace/RELEASE.zh-CN.md)
- [调度模式](docs/userspace/SCHEDULER-MODES.md)
- [部署与回滚](docs/userspace/DEPLOYMENT.zh-CN.md)
- [快速开始](docs/guides/QUICKSTART.zh-CN.md)
- [故障排查](docs/guides/TROUBLESHOOTING.zh-CN.md)

- [完整 Provisioning API](docs/userspace/PROVISIONING.md)
