# MPTCP Userspace

[English](README.md)

MPTCP Userspace 是一个运行在应用层的多路径传输系统，由 macOS 客户端 **MPTCP Desk** 和 Linux **Landing** 组成。它把多条普通 TCP carrier 聚合到同一个经过认证的 MPX/3 会话中，再把应用 TCP 业务流复用到这些路径上。

它**不是内核 MPTCP，也不是 QUIC**。macOS Userspace 模式使用普通 TCP carrier；Linux Landing 负责终止 MPX/3，再将业务字节转发到 backend。

当前正式版本：**v0.9.4 / MPX/3 capability revision 5**。

- Release：https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.9.4
- macOS：arm64/x86_64 Universal DMG，macOS 13+
- Landing：Linux amd64 静态二进制
- 调度模式：Auto / Aggregate / Protect / Weighted

## 它解决什么问题

典型拓扑：

~~~text
应用 / Surge
    |
    v
127.0.0.1:1081
透明 TCP 入口（不是 SOCKS5）
    |
    v
MPTCP Desk / Userspace Engine
    |
    +---- 普通 TCP carrier ---- Relay A ----+
    +---- 普通 TCP carrier ---- Relay B ----+---- Linux Landing ---- Backend
    +---- 普通 TCP carrier ---- Relay C ----+
    +---- ...
~~~

Relay 只需要转发普通 TCP 字节，不理解 MPX/3。认证、逻辑流复用、调度、信用控制、重传和跨路 reinjection 都由 Mac 与 Landing 完成。

这使得多条独立 Relay 链路可以作为一个逻辑传输会话使用，同时仍保留对慢路、断路、排队和交付超时的保护。

## 四种调度模式

| 模式 | 适合场景 | 容量依据 |
|---|---|---|
| **Auto** | 默认。线路接近时保持 Aggregate；出现稳定异构或交付故障时转向 Protect 行为，并带滞回恢复。 | 在线学习 |
| **Aggregate** | 多条线路质量接近，希望尽可能利用总吞吐。 | 在线学习 |
| **Protect** | 路径差异明显，希望限制异常/慢路径积压，允许路径进入 PROBE/BACKUP。 | 在线学习 |
| **Weighted** | 已经知道每条固定 Relay 的实际带宽能力，希望避免在线估速误差长期把流量偏到少数路径。 | 用户配置 |

### Weighted

每条 Relay 可配置：

- download_mbps：**必填**，代表 Landing → Mac 的可用下行能力；
- upload_mbps：**选填**，代表 Mac → Landing 的可用上行能力；
- 上行留空：只有 Mac → Landing 方向回退到 Aggregate 的在线估速；
- 合法范围：0.1–6553.5 Mbps，最多 1 位小数。

Weighted 不是“严格按比例无脑分流”。配置带宽主要替代正常 DATA 调度的容量先验和 flight budget；以下实时保护仍然有效：

- RTT / minRTT；
- writer queue 债务；
- carrier 连接状态；
- path penalty；
- delivery timeout；
- reinjection；
- retransmission。

因此某条路径在满载时 RTT 从例如 30 ms 上升到更高水平，或者出现队列/交付异常时，即使它配置了较大的带宽，调度器也不会完全忽略这些实时信号。

## 版本兼容

| Mac | Landing | Auto / Aggregate / Protect | Weighted |
|---|---|---:|---:|
| 0.9.4 | 0.9.4 | 支持 | 支持 |
| 0.9.4 | 0.9.3 | 支持 | 不支持 |
| 0.9.3 | 0.9.4 | 支持 | 不支持 |
| MPX/2 / 更早候选 | MPX/3 Rev5 | 不兼容 | 不兼容 |

0.9.4 的 Auto/Aggregate/Protect 继续使用 0.9.3 的 0x41 / 0x42 / 0x43 hello，Weighted 使用 0x44 和认证过的方向容量字段。

**Weighted 必须 Mac 与 Landing 双端都升级到 0.9.4。**

## 资源边界

0.9.4 没有通过扩大资源上限来获得性能：

- 最多 2048 个占用中的逻辑流身份；
- 128 MiB session credit；
- 128 MiB sender DATA pending；
- 128 MiB 物理接收页记账上限；
- 单流 receive window 最大 16 MiB；
- DATA payload 最大 32 KiB。

详细机制见 [MPX/3 信用控制](docs/userspace/MPX3-CREDIT.md) 和 [协议说明](docs/userspace/PROTOCOL.md)。

## 快速开始

建议按以下文档顺序：

1. [快速开始](docs/guides/QUICKSTART.zh-CN.md)
2. [架构说明](docs/guides/ARCHITECTURE.zh-CN.md)
3. [部署与回滚](docs/userspace/DEPLOYMENT.zh-CN.md)
4. [调度策略详解](docs/userspace/SCHEDULER-MODES.md)
5. [故障排查](docs/guides/TROUBLESHOOTING.zh-CN.md)

已有托管 Landing 可使用受控升级：

~~~sh
chmod 755 ./mptcp-landing
./mptcp-landing version
./mptcp-landing upgrade --source ./mptcp-landing --sha256 <可信的完整SHA256>
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
~~~

管理器会保留上一份二进制及其配套配置，可以使用：

~~~sh
/usr/local/bin/mptcp-landing rollback
~~~

进行回滚。

## macOS 入口

Userspace 模式默认使用本地 127.0.0.1:1081 作为透明 TCP 入口。

**它不是 SOCKS5 服务。**

应继续使用原 Surge / SS / AnyTLS 等上层代理配置，将相应 TCP 连接交给这个透明入口；不要因为看到 1081 就把它当成普通 SOCKS5 端口。

关闭 App 主窗口不会自动停止转发，应通过菜单栏状态确认当前运行状态。

## 安全模型

MPX/3 使用 PSK 认证握手，并为两个方向派生独立 AES-GCM key/counter。scheduler mode 和 Weighted 方向容量都属于认证 transcript，链路中间设备不能静默篡改这些值。

同时要明确：

- 这不是 TLS PKI；
- 0.9.4 不提供 forward secrecy；
- 没有宣称经过独立安全认证；
- transport key 不应提交到 Git 仓库、Issue、聊天记录或公开日志；
- macOS DMG 为 ad-hoc 签名，**未 notarize**。

## 0.9.4 验证边界

v0.9.4 已完成与冻结 Source-ID 匹配的：

- Go / Swift 正确性与回归门禁；
- Weighted 方向容量认证；
- 配置校验；
- 旧三模式兼容回归；
- disconnect / penalty / delivery timeout 保护；
- source-matched 实验室 Weighted 高 BDP 回归。

本次正式 Weighted release **没有把完整 30 秒 capacity matrix 和真实 180 秒 App+Surge/WAN 现场验收声明为已完成**。

准确状态以 Release 中这些文件为准：

- ACCEPTANCE.md
- TESTS.json
- SCHEDULER-MODES.json
- PROVENANCE.json

实验室结果不代表任意公网线路速度、多日稳定性或运营商 SLA。

## Release 来源与 main 分支

v0.9.4 Tag 固定指向生成 Release 二进制的冻结源码：

~~~text
Commit:    f97f810b393c5f67dca02607ccefb41d78c7c169
Source-ID: d4b8f362a8179257f2889abfea587c480755470bac6a78d8c023c3496c66f2bf
~~~

main 在正式 Tag 之后可以继续增加**文档类提交**。因此复现或审计 v0.9.4 二进制时，应以 v0.9.4 Tag 与对应 Source-ID 为准，而不是假定最新 main 的文档树仍与冻结 Source-ID 完全相同。

## 文档入口

完整索引见 [docs/README.zh-CN.md](docs/README.zh-CN.md)。

常用文档：

- [快速开始](docs/guides/QUICKSTART.zh-CN.md)
- [从源码构建](docs/guides/BUILDING.zh-CN.md)
- [架构](docs/guides/ARCHITECTURE.zh-CN.md)
- [协议](docs/userspace/PROTOCOL.md)
- [调度模式](docs/userspace/SCHEDULER-MODES.md)
- [信用控制](docs/userspace/MPX3-CREDIT.md)
- [部署与回滚](docs/userspace/DEPLOYMENT.zh-CN.md)
- [验证边界](docs/userspace/VALIDATION.md)
- [故障排查](docs/guides/TROUBLESHOOTING.zh-CN.md)
- [0.9.4 Release Notes](docs/userspace/RELEASE.zh-CN.md)
