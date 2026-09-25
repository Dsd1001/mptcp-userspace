# 快速开始

[English](QUICKSTART.md)

本文按 GitHub Release v0.9.4 的正式交付物说明最短部署路径。假设你已经有可用的 TCP Relay，并有一台 Linux amd64 机器作为 Landing。

Release：https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.9.4

## 1. 下载并校验

至少下载：

- MPTCP-Desk-0.9.4-universal.dmg
- mptcp-landing
- MPTCP-Desk-0.9.4-SHA256SUMS
- mptcp-landing.sha256

macOS 上校验 DMG：

~~~sh
shasum -a 256 -c MPTCP-Desk-0.9.4-SHA256SUMS
~~~

Linux 上校验 Landing：

~~~sh
sha256sum -c mptcp-landing.sha256
~~~

v0.9.4 的冻结 Source-ID：

~~~text
d4b8f362a8179257f2889abfea587c480755470bac6a78d8c023c3496c66f2bf
~~~

如果校验不一致，不要继续安装。

## 2. 准备 Landing

在 Linux amd64 上，管理操作使用 root 或具备相应 sudo 权限的账号：

~~~sh
chmod 755 ./mptcp-landing
./mptcp-landing version
./mptcp-landing menu
~~~

交互管理器提供：

- install
- config
- start / stop / restart
- status
- logs
- doctor
- upgrade
- rollback
- uninstall

对已经托管的旧 Landing，可以直接走受控升级：

~~~sh
./mptcp-landing upgrade --source ./mptcp-landing --sha256 <可信的完整SHA256>
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
~~~

升级会保留上一份二进制和配套配置，便于 rollback。

不要把 transport key 放进公开仓库、Issue、聊天或日志。除非你明确要改拓扑，否则沿用原 backend、transport key、Relay 与端口模型。

## 3. 安装 macOS Client

DMG 是 macOS 13+ 的 arm64/x86_64 Universal 构建。

建议流程：

1. 先停止旧版正在运行的转发。
2. 打开 DMG。
3. 用新版本替换 Applications 中的 MPTCP Desk。
4. 启动 App，按正常 macOS / Keychain 流程授权。
5. 不要为了打开 App 去关闭 SIP 或 Gatekeeper。

当前 DMG 是 ad-hoc 签名，未 notarize。

## 4. 配置 Userspace

本地入口通常是：

~~~text
127.0.0.1:1081
~~~

这是**透明 TCP 入口，不是 SOCKS5**。

继续让原 Surge / SS / AnyTLS 等上层配置把相应 TCP 连接交给该入口即可。

然后配置 Relay 列表，并选择调度模式：

- **Auto**：默认，通常优先推荐作为通用模式。
- **Aggregate**：固定使用高吞吐学习调度。
- **Protect**：固定使用路径保护/角色机制。
- **Weighted**：已知每条 Relay 带宽能力时使用。

### Weighted 配置

每条 Relay：

- 下行 Mbps：必填；
- 上行 Mbps：选填。

方向含义：

- 下行 = Landing → Mac；
- 上行 = Mac → Landing。

如果上行留空，则只有 Mac → Landing 这个方向继续使用 Aggregate 在线估速。

## 5. 检查版本兼容

Weighted 必须双端都是 0.9.4。

0.9.4 的 Auto / Aggregate / Protect 继续沿用 0.9.3 的 hello，因此这些模式可以与 0.9.3 对接。

MPX/2、Rev2/Rev3 候选和更早协议不能与 MPX/3 Rev5 混连。

## 6. 启动后检查

Landing：

~~~sh
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
~~~

macOS 诊断页重点看：

- configured scheduler；
- effective scheduler；
- carrier 数量和连接状态；
- 每条路径 RTT / minRTT；
- writer queue；
- role / role reason；
- penalty / timeout；
- Weighted rate；
- retransmission / reinjection；
- lifecycle 记录。

Weighted 中“配置了 50 Mbps”并不等于该路径永远必须跑满 50 Mbps。真实 RTT、queue、断线、penalty、delivery timeout 等仍会覆盖静态容量先验。

## 7. 回滚

Landing 托管升级后：

~~~sh
/usr/local/bin/mptcp-landing rollback
~~~

如果 Landing 回滚到 0.9.3 或更早版本，Mac 也必须退出 Weighted；只使用 Auto/Aggregate/Protect 时按兼容矩阵处理。

更多细节：

- [部署与回滚](../userspace/DEPLOYMENT.zh-CN.md)
- [架构说明](ARCHITECTURE.zh-CN.md)
- [调度策略](../userspace/SCHEDULER-MODES.md)
- [故障排查](TROUBLESHOOTING.zh-CN.md)
